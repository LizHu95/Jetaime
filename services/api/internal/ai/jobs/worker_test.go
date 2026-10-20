package jobs

import (
	"context"
	"testing"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	aicontext "github.com/LizHu95/Jetaime/services/api/internal/ai/context"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
)

type failureStore struct {
	code, answer string
	completions  int
}

func (*failureStore) Claim(context.Context, time.Duration) (ai.Job, error) {
	return ai.Job{}, ai.ErrNotFound
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
