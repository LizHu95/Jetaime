package jobs

import (
	"context"
	"testing"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	aicontext "github.com/LizHu95/Jetaime/services/api/internal/ai/context"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
	"github.com/LizHu95/Jetaime/services/api/internal/telemetry"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

type failureStore struct {
	code, answer string
	completions  int
}

type tracingExecutor struct{}

func (tracingExecutor) Execute(ctx context.Context, _ ai.Invocation) (ai.Run, error) {
	ctx, parent := telemetry.Start(ctx, "ai.invoke", "CHAIN")
	_, child := telemetry.Start(ctx, "llm.call", "LLM")
	telemetry.End(child, nil)
	telemetry.End(parent, nil)
	return ai.Run{}, ai.ErrInvalidDecision
}

func TestWorkerTracesDurableTriggerSourceOnEveryNode(t *testing.T) {
	previous := otel.GetTracerProvider()
	t.Cleanup(func() { otel.SetTracerProvider(previous) })
	t.Setenv("JETAIME_TRACE_CAPTURE_CONTENT", "false")
	for _, tc := range []struct {
		name, storedMode string
		mentions         bool
		mode, source     string
	}{
		{"proactive", "proactive", false, "proactive", "proactive"},
		{"mention", "mention", true, "reactive", "mention"},
		{"reminder followup", "mention", false, "reactive", "reminder_followup"},
		{"missing legacy metadata", "", false, "unknown", "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exporter := tracetest.NewInMemoryExporter()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
			otel.SetTracerProvider(provider)
			defer func() {
				if err := provider.Shutdown(context.Background()); err != nil {
					t.Error(err)
				}
			}()
			store := &failureStore{}
			New(store, tracingExecutor{}, 16384, time.Second).Execute(context.Background(), ai.Job{ID: "job", GroupID: "group", RequestedBy: "user", Kind: "auto", TriggerMode: tc.storedMode, TriggerMentionsAI: tc.mentions, Attempt: 2})
			spans := exporter.GetSpans()
			if len(spans) != 5 {
				t.Fatalf("unexpected execution spans: %d", len(spans))
			}
			for _, span := range spans {
				attrs := map[string]attribute.Value{}
				for _, attr := range span.Attributes {
					attrs[string(attr.Key)] = attr.Value
				}
				if attrs["chat.trigger_mode"].AsString() != tc.mode || attrs["chat.trigger_source"].AsString() != tc.source || attrs["chat.trigger_mentions_ai"].AsBool() != tc.mentions {
					t.Fatalf("trigger metadata missing on %s: %v", span.Name, attrs)
				}
				if _, ok := attrs["chat.trigger_mentions_ai"]; !ok {
					t.Fatalf("missing false mention flag on %s", span.Name)
				}
			}
		})
	}
}

func (*failureStore) Claim(context.Context, time.Duration) (ai.Job, error) {
	return ai.Job{}, ai.ErrNotFound
}
func (*failureStore) CheckIntervention(context.Context, ai.Job, string, ai.Recognition) (string, error) {
	return "", nil
}
func (*failureStore) AgentContext(context.Context, ai.Job, int, ...aicontext.ContextInjector) (ai.Context, string, error) {
	return ai.Context{}, "明天", nil
}
func (*failureStore) ExtractionContext(context.Context, ai.Job, int) (ai.Context, error) {
	return ai.Context{}, nil
}
func (s *failureStore) Complete(_ context.Context, _ ai.Job, answer, code string, _ ...*ai.Result) error {
	s.code = code
	s.answer = answer
	s.completions++
	return nil
}

type failureExecutor struct{ err error }

func (e failureExecutor) Execute(context.Context, ai.Invocation) (ai.Run, error) {
	return ai.Run{}, e.err
}

func TestWorkerPreservesSafeReminderErrorReasons(t *testing.T) {
	for _, tc := range []struct{ code, want string }{
		{"invalid_sources", "invalid_sources"},
		{"time_evidence_missing", "time_evidence_missing"},
		{"invalid_arguments", "invalid_model_response"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			store := &failureStore{}
			worker := New(store, failureExecutor{err: tools.InvalidArguments(tc.code, "validation feedback")}, 16384, time.Second)
			worker.Execute(context.Background(), ai.Job{ID: "run", GroupID: "group", RequestedBy: "user", Kind: "auto"})
			if store.completions != 1 || store.code != tc.want || store.answer != "" {
				t.Fatalf("error persisted incorrectly: %+v", store)
			}
		})
	}
}
