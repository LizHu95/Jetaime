// Package telemetry 提供可选的本地调试追踪；不改变业务结果或错误语义。
package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// Config 只控制观测能力。开启后包含完整的已授权调试输入输出，默认关闭。
type Config struct {
	Enabled  bool
	Endpoint string    // 完整 OTLP HTTP 地址，默认 http://127.0.0.1:6006/v1/traces。
	Project  string    // Phoenix 中区分当前应用的项目名。
	Warnings io.Writer // 导出失败提示，不包含业务数据；CLI 使用 stderr。
}

type tracerKey struct{}

// Init 将独立 Tracer 放进 context，不修改全局 OTel 配置，避免测试或其他服务串扰。
// 导出使用异步有界队列；Phoenix 离线不会阻止业务执行。退出时调用 shutdown。
func Init(ctx context.Context, config Config) (context.Context, func(context.Context) error, error) {
	if !config.Enabled {
		return ctx, func(context.Context) error { return nil }, nil
	}
	provider, err := NewProvider(ctx, config)
	if err != nil {
		return ctx, nil, err
	}
	return WithTracer(ctx, provider.Tracer("jetaime.workflow")), provider.Shutdown, nil
}

// NewProvider 让页面的同步 recorder 与 Phoenix 的异步 exporter 共用同一批 span。
// options 中的本地 processor 即使关闭 Phoenix 也继续记录；调用方负责服务退出时 Shutdown。
func NewProvider(ctx context.Context, config Config, options ...sdktrace.TracerProviderOption) (*sdktrace.TracerProvider, error) {
	options = append(options, sdktrace.WithSampler(sdktrace.AlwaysSample()))
	if !config.Enabled {
		return sdktrace.NewTracerProvider(options...), nil
	}
	u, err := url.Parse(config.Endpoint)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path == "" {
		return nil, fmt.Errorf("trace: invalid OTLP HTTP endpoint")
	}
	if config.Project == "" {
		return nil, fmt.Errorf("trace: project name is required")
	}
	exporter, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpointURL(config.Endpoint),
		otlptracehttp.WithTimeout(2*time.Second),
		otlptracehttp.WithRetry(otlptracehttp.RetryConfig{Enabled: false}),
	)
	if err != nil {
		return nil, fmt.Errorf("trace: initialize exporter: %w", err)
	}
	options = append(options,
		sdktrace.WithResource(resource.NewSchemaless(attribute.String("service.name", "jetaime-api"), attribute.String("openinference.project.name", config.Project))),
		sdktrace.WithBatcher(&bestEffortExporter{SpanExporter: exporter, warnings: config.Warnings}, sdktrace.WithBatchTimeout(500*time.Millisecond), sdktrace.WithMaxQueueSize(512)),
	)
	return sdktrace.NewTracerProvider(options...), nil
}

// bestEffortExporter 将上报故障与业务故障隔离；报告丢失但不伪造业务失败。
type bestEffortExporter struct {
	sdktrace.SpanExporter
	warnings io.Writer
}

func (e *bestEffortExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	if err := e.SpanExporter.ExportSpans(ctx, spans); err != nil && e.warnings != nil {
		_, _ = fmt.Fprintln(e.warnings, "trace: Phoenix 上报失败，部分追踪未保存；业务运行不受影响")
	}
	return nil
}

// WithTracer 也供测试注入内存 exporter，不需要启动 Phoenix。
func WithTracer(ctx context.Context, tracer trace.Tracer) context.Context {
	return context.WithValue(ctx, tracerKey{}, tracer)
}

// Start 遵循 OpenInference 属性约定，让 Phoenix 能展示步骤的输入输出。
func Start(ctx context.Context, name, kind string, input any) (context.Context, trace.Span) {
	tracer, ok := ctx.Value(tracerKey{}).(trace.Tracer)
	if !ok {
		tracer = noop.NewTracerProvider().Tracer("jetaime.workflow")
	}
	ctx, span := tracer.Start(ctx, name, trace.WithAttributes(attribute.String("openinference.span.kind", kind)))
	Input(span, input)
	return ctx, span
}

func Input(span trace.Span, value any)  { setJSON(span, "input", value) }
func Output(span trace.Span, value any) { setJSON(span, "output", value) }

func setJSON(span trace.Span, field string, value any) {
	if !span.IsRecording() || value == nil {
		return
	}
	data, err := json.Marshal(value)
	if err != nil {
		span.AddEvent("trace serialization failed")
		return
	}
	span.SetAttributes(attribute.String(field+".value", string(data)), attribute.String(field+".mime_type", "application/json"))
}

// Finish 必须在每条返回路径执行，错误也留下 span；观测错误不作为业务错误返回。
func Finish(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	} else {
		span.SetStatus(codes.Ok, "")
	}
	span.End()
}

// Step 包裹一个有输入输出的流程步骤，回调必须继续传递子 context 以保留父子关系。
func Step[T any](ctx context.Context, name string, input any, run func(context.Context) (T, error)) (output T, err error) {
	ctx, span := Start(ctx, name, "CHAIN", input)
	defer func() {
		if err == nil {
			Output(span, output)
		}
		Finish(span, err)
	}()
	return run(ctx)
}
