package chat

import (
	"context"
	"encoding/json"
	"fmt"
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
	if _, err := f.service.Send(ctx, f.group, f.users[0].ID, SendInput{Content: "@AI 测试", ClientID: uuid.NewString()}); err != nil {
		t.Fatal(err)
	}
	telemetry.End(parent, nil)
	run, err := f.store.Claim(context.Background(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !run.TriggerMentionsAI || run.TriggerMode != "mention" {
		t.Fatal("mention trigger metadata lost")
	}
	if run.TraceParent == "" || run.QueuedAt.IsZero() {
		t.Fatal("queue context not persisted")
	}
	f.worker(t).Execute(context.Background(), run)
	runs, err := f.store.Runs(context.Background(), f.group, f.users[0].ID)
	if err != nil || len(runs) != 1 || runs[0].TraceID == nil || *runs[0].TraceID != parent.SpanContext().TraceID().String() {
		t.Fatalf("execution trace not returned: %+v, %v", runs, err)
	}
	payload, err := json.Marshal(runs[0])
	if err != nil {
		t.Fatal(err)
	}
	var returned map[string]any
	if err := json.Unmarshal(payload, &returned); err != nil || returned["traceId"] != parent.SpanContext().TraceID().String() {
		t.Fatalf("traceId missing from API JSON: %s, %v", payload, err)
	}
	if _, leaked := returned["traceParent"]; leaked {
		t.Fatal("propagation context exposed")
	}
	spans := exporter.GetSpans()
	var enqueueID trace.SpanID
	for _, span := range spans {
		if span.Name == "chat.ai.enqueue" {
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

	queued, err := f.store.Runs(context.Background(), f.group, f.users[0].ID)
	if err != nil || queued[0].TraceID != nil {
		t.Fatalf("retry retained old trace: %+v, %v", queued, err)
	}
	retried, err := f.store.Claim(context.Background(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !retried.TriggerMentionsAI || retried.TriggerMode != "mention" {
		t.Fatal("retry changed original trigger metadata")
	}
	restored := telemetry.Restore(context.Background(), retried.TraceParent, retried.TraceState)
	_, attempt := telemetry.Start(restored, "attempt", "CHAIN")
	if attempt.SpanContext().TraceID() != retry.SpanContext().TraceID() {
		t.Fatal("retry parent not replaced")
	}
	telemetry.End(attempt, nil)
	f.worker(t).Execute(context.Background(), retried)
	runs, err = f.store.Runs(context.Background(), f.group, f.users[0].ID)
	if err != nil || runs[0].TraceID == nil || *runs[0].TraceID != retry.SpanContext().TraceID().String() {
		t.Fatalf("retry trace not replaced: %+v, %v", runs, err)
	}
}

func TestPostgresFailureTraceID(t *testing.T) {
	for _, sampled := range []bool{true, false} {
		t.Run(fmt.Sprint(sampled), func(t *testing.T) {
			f := setup(t)
			old := otel.GetTracerProvider()
			t.Cleanup(func() { otel.SetTracerProvider(old) })
			sampler := sdktrace.NeverSample()
			if sampled {
				sampler = sdktrace.AlwaysSample()
			}
			provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sampler))
			otel.SetTracerProvider(provider)
			defer func() { _ = provider.Shutdown(context.Background()) }()
			if _, err := f.service.Send(context.Background(), f.group, f.users[0].ID, SendInput{Content: "@AI test", MentionsAI: true, ClientID: uuid.NewString()}); err != nil {
				t.Fatal(err)
			}
			job, err := f.store.Claim(context.Background(), time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			// Simulate a recovered legacy job with no usable enqueue parent.
			ctx, span := telemetry.Start(context.Background(), "chat.ai.execute", "CHAIN")
			defer telemetry.End(span, nil)
			if err := f.store.Complete(ctx, job, "", "model_failed"); err != nil {
				t.Fatal(err)
			}
			runs, err := f.store.Runs(context.Background(), f.group, f.users[1].ID)
			if err != nil || len(runs) != 1 {
				t.Fatalf("runs: %+v, %v", runs, err)
			}
			if runs[0].Status != "failed" || runs[0].ErrorCode == nil || *runs[0].ErrorCode != "model_failed" {
				t.Fatalf("failure lost: %+v", runs[0])
			}
			if sampled && (runs[0].TraceID == nil || *runs[0].TraceID != span.SpanContext().TraceID().String()) {
				t.Fatal("actual failure trace not persisted")
			}
			if !sampled && runs[0].TraceID != nil {
				t.Fatal("unsampled trace presented as available")
			}

			// Failures before worker execution still have the enqueue trace.
			enqueueCtx, enqueue := telemetry.Start(context.Background(), "send", "CHAIN")
			message, err := NewService(f.store, false).Send(enqueueCtx, f.group, f.users[0].ID, SendInput{Content: "@AI unavailable", ClientID: uuid.NewString()})
			telemetry.End(enqueue, nil)
			if err != nil {
				t.Fatal(err)
			}
			runs, err = f.store.Runs(context.Background(), f.group, f.users[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, run := range runs {
				if run.TriggerID != message.ID {
					continue
				}
				found = true
				if sampled && (run.TraceID == nil || *run.TraceID != enqueue.SpanContext().TraceID().String()) {
					t.Fatal("enqueue failure trace lost")
				}
				if !sampled && run.TraceID != nil {
					t.Fatal("unsampled enqueue trace exposed")
				}
			}
			if !found {
				t.Fatal("missing enqueue failure")
			}
		})
	}
}
