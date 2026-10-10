package telemetry

import (
	"context"
	"errors"
	"testing"

	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestStepRecordsParentIOAndFailure(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	ctx := WithTracer(context.Background(), provider.Tracer("test"))
	ctx, root := Start(ctx, "root", "CHAIN", map[string]string{"query": "今晚吃什么"})
	wantErr := errors.New("check failed")
	if _, err := Step(ctx, "child", "input", func(context.Context) (string, error) { return "", wantErr }); !errors.Is(err, wantErr) {
		t.Fatal(err)
	}
	Output(root, map[string]string{"status": "failed"})
	Finish(root, wantErr)
	spans := exporter.GetSpans()
	if len(spans) != 2 || spans[0].Parent.SpanID() != spans[1].SpanContext.SpanID() || spans[0].SpanContext.TraceID() != spans[1].SpanContext.TraceID() {
		t.Fatal("incorrect hierarchy")
	}
	if spans[0].Status.Code != codes.Error || spans[1].Status.Code != codes.Error || len(spans[0].Events) == 0 {
		t.Fatal("errors not recorded")
	}
	attrs := map[string]string{}
	for _, a := range spans[1].Attributes {
		attrs[string(a.Key)] = a.Value.AsString()
	}
	if attrs["input.value"] != `{"query":"今晚吃什么"}` || attrs["output.value"] != `{"status":"failed"}` {
		t.Fatal("input/output missing", attrs)
	}
}

func TestDisabledDoesNotRequireEndpoint(t *testing.T) {
	ctx, shutdown, err := Init(context.Background(), Config{})
	if err != nil {
		t.Fatal(err)
	}
	_, span := Start(ctx, "disabled", "CHAIN", "private input")
	if span.IsRecording() {
		t.Fatal("tracing enabled by default")
	}
	Finish(span, nil)
	if err := shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Init(ctx, Config{Enabled: true, Endpoint: "invalid", Project: "test"}); err == nil {
		t.Fatal("invalid endpoint accepted")
	}
}
