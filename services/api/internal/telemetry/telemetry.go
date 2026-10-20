// Package telemetry configures optional AI tracing for Phoenix.
package telemetry

import (
	"context"
	"fmt"
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

// Init enables batched OTLP/HTTP export when a Phoenix endpoint is configured.
func Init(ctx context.Context) (func(context.Context) error, error) {
	noop := func(context.Context) error { return nil }
	endpoint := os.Getenv("JETAIME_PHOENIX_TRACES_ENDPOINT")
	if strings.EqualFold(os.Getenv("OTEL_SDK_DISABLED"), "true") || endpoint == "" {
		return noop, nil
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, fmt.Errorf("JETAIME_PHOENIX_TRACES_ENDPOINT must be an http(s) URL without credentials, query or fragment")
	}
	headers := map[string]string{}
	if key := os.Getenv("PHOENIX_API_KEY"); key != "" {
		headers["Authorization"] = "Bearer " + key
	}
	exporter, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(endpoint), otlptracehttp.WithHeaders(headers), otlptracehttp.WithTimeout(5*time.Second))
	if err != nil {
		return nil, fmt.Errorf("initialize Phoenix trace exporter")
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
		fmt.Fprintln(os.Stderr, "trace export failed; check trace backend configuration and connectivity")
	}))
	return provider.Shutdown, nil
}

type attributesKey struct{}

// Start carries only application-controlled identity fields to child observations.
func Start(ctx context.Context, name, kind string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	return StartAt(ctx, name, kind, time.Time{}, attrs...)
}

// StartAt preserves the measured time of a diagnostic step recorded after completion.
func StartAt(ctx context.Context, name, kind string, started time.Time, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	inherited, _ := ctx.Value(attributesKey{}).([]attribute.KeyValue)
	fields := make(map[attribute.Key]attribute.KeyValue)
	for _, attr := range inherited {
		fields[attr.Key] = attr
	}
	for _, attr := range attrs {
		switch attr.Key {
		case "session.id", "user.id", "chat.run_id":
			fields[attr.Key] = attr
		}
	}
	inherited = make([]attribute.KeyValue, 0, len(fields))
	for _, attr := range fields {
		inherited = append(inherited, attr)
	}
	ctx = context.WithValue(ctx, attributesKey{}, inherited)
	attrs = append(append([]attribute.KeyValue{}, attrs...), inherited...)
	attrs = append(attrs, attribute.String("openinference.span.kind", kind))
	options := []trace.SpanStartOption{trace.WithAttributes(attrs...)}
	if !started.IsZero() {
		options = append(options, trace.WithTimestamp(started))
	}
	return otel.Tracer("jetaime").Start(ctx, name, options...)
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
