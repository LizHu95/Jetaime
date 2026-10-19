// Package telemetry configures optional AI tracing for Langfuse Cloud.
package telemetry

import (
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// Init enables export only when both Langfuse project keys are configured.
func Init(ctx context.Context) (func(context.Context) error, error) {
	noop := func(context.Context) error { return nil }
	if strings.EqualFold(os.Getenv("OTEL_SDK_DISABLED"), "true") {
		return noop, nil
	}
	public, secret := os.Getenv("LANGFUSE_PUBLIC_KEY"), os.Getenv("LANGFUSE_SECRET_KEY")
	if public == "" && secret == "" {
		return noop, nil
	}
	if public == "" || secret == "" {
		return nil, fmt.Errorf("LANGFUSE_PUBLIC_KEY and LANGFUSE_SECRET_KEY must both be configured")
	}
	base := os.Getenv("LANGFUSE_BASE_URL")
	if base == "" {
		base = "https://cloud.langfuse.com"
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, fmt.Errorf("LANGFUSE_BASE_URL must be an http(s) URL without credentials, query or fragment")
	}
	if u.Scheme == "http" && u.Hostname() != "localhost" {
		ip := net.ParseIP(u.Hostname())
		if ip == nil || !ip.IsLoopback() {
			return nil, fmt.Errorf("LANGFUSE_BASE_URL requires HTTPS except on loopback")
		}
	}
	endpoint := strings.TrimRight(base, "/") + "/api/public/otel/v1/traces"
	headers := map[string]string{
		"Authorization":                "Basic " + base64.StdEncoding.EncodeToString([]byte(public+":"+secret)),
		"x-langfuse-ingestion-version": "4",
	}
	exporter, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(endpoint), otlptracehttp.WithHeaders(headers), otlptracehttp.WithTimeout(5*time.Second))
	if err != nil {
		return nil, fmt.Errorf("initialize Langfuse trace exporter")
	}

	name := os.Getenv("OTEL_SERVICE_NAME")
	if name == "" {
		name = "jetaime-api"
	}
	res, err := resource.New(ctx, resource.WithFromEnv(), resource.WithAttributes(attribute.String("service.name", name)))
	if err != nil {
		_ = exporter.Shutdown(ctx)
		return nil, fmt.Errorf("initialize trace resource: %w", err)
	}
	provider := sdktrace.NewTracerProvider(sdktrace.WithResource(res), sdktrace.WithBatcher(exporter, sdktrace.WithBatchTimeout(time.Second)))
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	// Exporter errors can contain endpoint/transport details. Keep credentials out of logs.
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(error) {
		fmt.Fprintln(os.Stderr, "trace export failed; check Langfuse keys, region and connectivity")
	}))
	return provider.Shutdown, nil
}

type attributesKey struct{}

// Start carries only application-controlled identity fields to child observations.
func Start(ctx context.Context, name, kind string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	inherited, _ := ctx.Value(attributesKey{}).([]attribute.KeyValue)
	fields := make(map[attribute.Key]attribute.KeyValue)
	for _, attr := range inherited {
		fields[attr.Key] = attr
	}
	for _, attr := range attrs {
		switch attr.Key {
		case "session.id", "user.id", "chat.run_id", "langfuse.trace.name":
			fields[attr.Key] = attr
		}
	}
	if _, ok := fields["langfuse.trace.name"]; !ok {
		fields["langfuse.trace.name"] = attribute.String("langfuse.trace.name", name)
	}
	inherited = make([]attribute.KeyValue, 0, len(fields))
	for _, attr := range fields {
		inherited = append(inherited, attr)
	}
	ctx = context.WithValue(ctx, attributesKey{}, inherited)
	attrs = append(append([]attribute.KeyValue{}, attrs...), inherited...)
	observationType := "span"
	switch kind {
	case "LLM":
		observationType = "generation"
	case "CHAIN":
		observationType = "chain"
	case "AGENT":
		observationType = "agent"
	case "EVALUATOR":
		observationType = "evaluator"
	}
	attrs = append(attrs, attribute.String("langfuse.observation.type", observationType))
	// Langfuse indexes explicit metadata fields for filtering.
	for _, attr := range attrs {
		key := string(attr.Key)
		if strings.HasPrefix(key, "chat.") || strings.HasPrefix(key, "workflow.") {
			attrs = append(attrs, attribute.KeyValue{Key: attribute.Key("langfuse.observation.metadata." + strings.ReplaceAll(key, ".", "_")), Value: attr.Value})
		}
	}
	return otel.Tracer("jetaime").Start(ctx, name, trace.WithAttributes(attrs...))
}

// End records a bounded error category; callers must not pass provider bodies or secrets.
func End(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	} else {
		span.SetStatus(codes.Ok, "")
	}
	span.End()
}

// Content is opt-in and bounded independently of the model context window.
func Content(span trace.Span, key, value string) {
	if !span.IsRecording() || !strings.EqualFold(os.Getenv("JETAIME_TRACE_CAPTURE_CONTENT"), "true") {
		return
	}
	const limit = 64 * 1024
	if len(value) > limit {
		value = value[:limit]
		for !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
		span.SetAttributes(attribute.Bool(key+".truncated", true))
	}
	span.SetAttributes(attribute.String(key, value))
}

// Carrier persists only W3C trace context, never baggage or request headers.
func Carrier(ctx context.Context) propagation.MapCarrier {
	carrier := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, carrier)
	return carrier
}
func Restore(ctx context.Context, parent, state string) context.Context {
	return propagation.TraceContext{}.Extract(ctx, propagation.MapCarrier{"traceparent": parent, "tracestate": state})
}
