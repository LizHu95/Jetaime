package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/telemetry"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestModelSpansUsageParentAndSanitizedFailure(t *testing.T) {
	old := otel.GetTracerProvider()
	t.Cleanup(func() { otel.SetTracerProvider(old) })
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	otel.SetTracerProvider(provider)
	defer func() { _ = provider.Shutdown(context.Background()) }()
	t.Setenv("JETAIME_TRACE_CAPTURE_CONTENT", "true")
	t.Setenv("TEST_LLM_KEY", "never-export-this-key")
	failed := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failed {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"error":"never-export-this-key"}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"{}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`))
	}))
	defer server.Close()
	model, err := (Config{Provider: "openai-compatible", BaseURL: server.URL, Model: "test", APIKeyEnv: "TEST_LLM_KEY", OutputMode: "json_object", ContextTokens: 8192}).New(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx, root := telemetry.Start(context.Background(), "workflow", "CHAIN")
	if _, err := model.Call(ctx, testRequest()); err != nil {
		t.Fatal(err)
	}
	failed = true
	if _, err := model.Call(ctx, testRequest()); err == nil {
		t.Fatal("expected failure")
	}
	telemetry.End(root, nil)
	spans := exporter.GetSpans()
	if len(spans) != 3 {
		t.Fatalf("unexpected span count: %d", len(spans))
	}
	if spans[0].Parent.SpanID() != root.SpanContext().SpanID() || spans[1].Status.Code != codes.Error {
		t.Fatal("parent or failure status missing")
	}
	values := map[string]any{}
	for _, attr := range spans[0].Attributes {
		values[string(attr.Key)] = attr.Value.AsInterface()
	}
	if values["llm.token_count.prompt"] != int64(10) || values["llm.token_count.completion"] != int64(2) || values["output.value"] != "{}" || values["openinference.span.kind"] != "LLM" {
		t.Fatalf("missing model metadata: %v", values)
	}
	for _, span := range spans {
		for _, event := range span.Events {
			for _, attr := range event.Attributes {
				if strings.Contains(attr.Value.AsString(), "never-export-this-key") {
					t.Fatal("credential leaked in trace")
				}
			}
		}
	}
}
