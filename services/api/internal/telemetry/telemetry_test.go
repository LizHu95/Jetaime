package telemetry

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	collector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
)

func restoreGlobals(t *testing.T) {
	t.Helper()
	provider, propagator, handler := otel.GetTracerProvider(), otel.GetTextMapPropagator(), otel.GetErrorHandler()
	t.Cleanup(func() {
		otel.SetTracerProvider(provider)
		otel.SetTextMapPropagator(propagator)
		otel.SetErrorHandler(handler)
	})
}

func TestOTLPHTTPExportAndDurableParent(t *testing.T) {
	restoreGlobals(t)
	received := make(chan *collector.ExportTraceServiceRequest, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		public, secret, ok := r.BasicAuth()
		if r.URL.Path != "/api/public/otel/v1/traces" || !ok || public != "test-public" || secret != "test-secret" || r.Header.Get("x-langfuse-ingestion-version") != "4" {
			t.Errorf("wrong OTLP path/auth")
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		data := &collector.ExportTraceServiceRequest{}
		if err := proto.Unmarshal(body, data); err != nil {
			t.Error(err)
		}
		received <- data
		w.Header().Set("Content-Type", "application/x-protobuf")
	}))
	defer server.Close()
	t.Setenv("OTEL_SDK_DISABLED", "false")
	t.Setenv("LANGFUSE_BASE_URL", server.URL)
	t.Setenv("LANGFUSE_PUBLIC_KEY", "test-public")
	t.Setenv("LANGFUSE_SECRET_KEY", "test-secret")
	t.Setenv("OTEL_TRACES_SAMPLER", "always_on")
	shutdown, err := Init(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, root := Start(context.Background(), "enqueue", "CHAIN")
	carrier := Carrier(ctx)
	if carrier["traceparent"] == "" {
		t.Fatal("missing persisted traceparent")
	}
	rootID := root.SpanContext()
	End(root, nil)
	// Simulate a new worker process context after the HTTP request ended.
	ctx = Restore(context.Background(), carrier["traceparent"], carrier["tracestate"])
	_, child := Start(ctx, "worker", "CHAIN")
	if child.SpanContext().TraceID() != rootID.TraceID() {
		t.Fatal("queue lost trace identity")
	}
	End(child, fmt.Errorf("model_failed"))
	if err := shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	var data *collector.ExportTraceServiceRequest
	select {
	case data = <-received:
	case <-time.After(5 * time.Second):
		t.Fatal("no OTLP batch received")
	}
	var names []string
	for _, res := range data.ResourceSpans {
		project := false
		for _, attr := range res.Resource.Attributes {
			if attr.Key == "service.name" && attr.Value.GetStringValue() == "jetaime-api" {
				project = true
			}
		}
		if !project {
			t.Error("service resource missing")
		}
		for _, scope := range res.ScopeSpans {
			for _, span := range scope.Spans {
				names = append(names, span.Name)
				if span.Name == "worker" {
					id := rootID.SpanID()
					if !bytes.Equal(span.ParentSpanId, id[:]) || span.Status.Code != 2 {
						t.Error("exported parent or error status lost")
					}
				}
			}
		}
	}
	if strings.Join(names, ",") != "enqueue,worker" {
		t.Fatalf("unexpected exported spans: %v", names)
	}
}

func TestDisabledAndInvalidEndpoint(t *testing.T) {
	restoreGlobals(t)
	t.Setenv("LANGFUSE_PUBLIC_KEY", "")
	t.Setenv("LANGFUSE_SECRET_KEY", "")
	t.Setenv("OTEL_SDK_DISABLED", "false")
	shutdown, err := Init(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LANGFUSE_PUBLIC_KEY", "test-public")
	if _, err := Init(context.Background()); err == nil {
		t.Fatal("partial Langfuse credentials accepted")
	}
	t.Setenv("LANGFUSE_SECRET_KEY", "test-secret")
	t.Setenv("LANGFUSE_BASE_URL", "http://secret:password@example.com")
	if _, err := Init(context.Background()); err == nil || strings.Contains(err.Error(), "password") {
		t.Fatal("invalid endpoint accepted or leaked")
	}
	t.Setenv("OTEL_SDK_DISABLED", "true")
	if _, err := Init(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestContentAndInheritedAttributes(t *testing.T) {
	restoreGlobals(t)
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	otel.SetTracerProvider(provider)
	defer func() { _ = provider.Shutdown(context.Background()) }()
	t.Setenv("JETAIME_TRACE_CAPTURE_CONTENT", "false")
	ctx, root := Start(context.Background(), "chat.ai", "CHAIN", attribute.String("session.id", "group-1"), attribute.String("user.id", "user-1"), attribute.String("chat.run_id", "run-1"))
	_, span := Start(ctx, "llm.call", "LLM")
	Content(span, "hidden", "private")
	t.Setenv("JETAIME_TRACE_CAPTURE_CONTENT", "true")
	Content(span, "langfuse.observation.input", strings.Repeat("你", 30000))
	End(span, nil)
	End(root, nil)
	attrs := map[string]attribute.Value{}
	for _, attr := range exporter.GetSpans()[0].Attributes {
		attrs[string(attr.Key)] = attr.Value
	}
	if _, ok := attrs["hidden"]; ok {
		t.Fatal("content exported without opt-in")
	}
	value := attrs["langfuse.observation.input"].AsString()
	if len(value) > 65536 || !utf8.ValidString(value) || !attrs["langfuse.observation.input.truncated"].AsBool() {
		t.Fatal("unbounded or invalid content")
	}
	if attrs["langfuse.observation.type"].AsString() != "generation" || attrs["session.id"].AsString() != "group-1" || attrs["user.id"].AsString() != "user-1" || attrs["langfuse.observation.metadata.chat_run_id"].AsString() != "run-1" {
		t.Fatal("Langfuse type or inherited identity missing")
	}
	if trace.SpanContextFromContext(Restore(context.Background(), "invalid", "")).IsValid() {
		t.Fatal("invalid context accepted")
	}
}
