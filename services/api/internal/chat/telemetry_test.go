package chat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
	"github.com/LizHu95/Jetaime/services/api/internal/telemetry"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestPostgresTraceSurvivesQueueAndRetry(t *testing.T) {
	f := setup(t)
	fake := f.model
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []llm.Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		out, err := fake.Call(r.Context(), llm.Request{Messages: request.Messages})
		if err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": out.Content}, "finish_reason": "stop"}},
			"usage":   map[string]int{"prompt_tokens": 10, "completion_tokens": 2, "total_tokens": 12},
		})
	}))
	defer modelServer.Close()
	t.Setenv("TRACE_TEST_MODEL_KEY", "test-only")
	model, err := (llm.Config{Provider: "openai-compatible", BaseURL: modelServer.URL, Model: "test", APIKeyEnv: "TRACE_TEST_MODEL_KEY", OutputMode: "json_object", ContextTokens: 8192}).New(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	f.model = model
	old := otel.GetTracerProvider()
	t.Cleanup(func() { otel.SetTracerProvider(old) })
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	otel.SetTracerProvider(provider)
	defer func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	if _, err := f.service.Send(context.Background(), f.group, f.users[0].ID, SendInput{Content: "普通消息", ClientID: uuid.NewString()}); err != nil {
		t.Fatal(err)
	}
	if len(exporter.GetSpans()) != 0 {
		t.Fatal("ordinary message consumed tracing quota")
	}
	ctx, parent := telemetry.Start(context.Background(), "send", "CHAIN")
	if _, err := f.service.Send(ctx, f.group, f.users[0].ID, SendInput{AgentKind: "qa", Content: "@AI 测试", ClientID: uuid.NewString()}); err != nil {
		t.Fatal(err)
	}
	telemetry.End(parent, nil)
	run, err := f.store.Claim(context.Background(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if run.TraceParent == "" || run.QueuedAt.IsZero() {
		t.Fatal("queue context not persisted")
	}
	f.worker(t).Execute(context.Background(), run)
	spans := exporter.GetSpans()
	var enqueueID trace.SpanID
	for _, span := range spans {
		if span.Name == "chat.ai" {
			enqueueID = span.SpanContext.SpanID()
		}
	}
	found := false
	for _, span := range spans {
		if span.Name == "chat.ai.execute" {
			found = true
			if span.Parent.SpanID() != enqueueID || span.SpanContext.TraceID() != parent.SpanContext().TraceID() {
				t.Fatal("worker disconnected from request")
			}
		}
	}
	if !found {
		t.Fatal("missing worker span")
	}
	// A retry should attach to the retry request instead of the original request.
	if _, err := f.store.pool.Exec(context.Background(), `UPDATE agent_runs SET status='failed',response_message_id=NULL,error_code='test_failure' WHERE id=$1`, run.ID); err != nil {
		t.Fatal(err)
	}
	retryCtx, retry := telemetry.Start(context.Background(), "retry", "CHAIN")
	if err := f.store.Retry(retryCtx, f.group, f.users[0].ID, run.ID); err != nil {
		t.Fatal(err)
	}
	telemetry.End(retry, nil)
	retried, err := f.store.Claim(context.Background(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	restored := telemetry.Restore(context.Background(), retried.TraceParent, retried.TraceState)
	_, attempt := telemetry.Start(restored, "attempt", "CHAIN")
	if attempt.SpanContext().TraceID() != retry.SpanContext().TraceID() {
		t.Fatal("retry parent not replaced")
	}
	telemetry.End(attempt, nil)
}
