package jobs

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	aicontext "github.com/LizHu95/Jetaime/services/api/internal/ai/context"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
	"github.com/LizHu95/Jetaime/services/api/internal/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

type Store interface {
	Claim(context.Context, time.Duration) (ai.Job, error)
	AgentContext(context.Context, ai.Job, int, ...aicontext.ContextInjector) (ai.Context, string, error)
	ExtractionContext(context.Context, ai.Job, int) (ai.Context, error)
	Complete(context.Context, ai.Job, string, string, ...*ai.Result) error
}
type Worker struct {
	store           Store
	executor        ai.Executor
	tokens          int
	timeout         time.Duration
	contextInjector aicontext.ContextInjector
}

func New(store Store, executor ai.Executor, tokens int, timeout time.Duration, injectors ...aicontext.ContextInjector) *Worker {
	w := &Worker{store: store, executor: executor, tokens: tokens, timeout: timeout}
	if len(injectors) > 0 {
		w.contextInjector = injectors[0]
	}
	return w
}
func (s *Worker) Work(ctx context.Context) {
	if s.executor == nil {
		return
	}
	for ctx.Err() == nil {
		run, err := s.store.Claim(ctx, s.timeout+30*time.Second)
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(500 * time.Millisecond):
			}
			continue
		}
		s.Execute(ctx, run)
	}
}
func (s *Worker) Execute(ctx context.Context, run ai.Job) {
	ctx = telemetry.Restore(ctx, run.TraceParent, run.TraceState)
	ctx, span := telemetry.Start(ctx, "chat.ai.execute", "CHAIN", attribute.String("user.id", run.RequestedBy), attribute.String("chat.run_id", run.ID), attribute.String("session.id", run.GroupID), attribute.String("chat.trigger_message_id", run.TriggerID), attribute.Int("chat.attempt", run.Attempt))
	if !run.QueuedAt.IsZero() {
		span.SetAttributes(attribute.Int64("chat.queue_wait_ms", time.Since(run.QueuedAt).Milliseconds()))
	}
	var traceErr error
	defer func() { telemetry.End(span, traceErr) }()
	call, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	contextCall, contextSpan := telemetry.Start(call, "chat.build_context", "CHAIN")
	input, question, err := s.store.AgentContext(contextCall, run, s.tokens, s.contextInjector)
	var contextErr error
	if err != nil {
		contextErr = errors.New("context unavailable")
	}
	telemetry.End(contextSpan, contextErr)
	code := ""
	answer := ""
	var result *ai.Result
	if errors.Is(err, ai.ErrContext) {
		code = "context_too_large"
	} else if err != nil {
		code = "context_unavailable"
	} else {
		mode := run.Kind
		if mode == "extract" {
			// Drain/retry previously persisted jobs through the current main entry.
			mode = "auto"
		}
		output, e := s.executor.Execute(call, ai.Invocation{AgentID: "main", ContextEndSeq: run.ContextEnd, Context: input, Mode: mode, Question: question, LoadExtraction: func(ctx context.Context) (ai.Context, error) {
			return s.store.ExtractionContext(ctx, run, s.tokens)
		}})
		if e != nil {
			log.Printf("chat agent run=%s kind=%s failed: %v", run.ID, run.Kind, e)
			code = "agent_failed"
			for _, step := range output.Steps {
				if step.Status == "failed" && step.Name == "model_call" {
					code = "model_failed"
				}
				if step.Status == "failed" && step.Name == "validate_result" {
					code = "invalid_model_response"
				}
			}
			if errors.Is(e, ai.ErrInvalidDecision) {
				code = "invalid_model_response"
			}
			if detail, ok := tools.Repairable(e); ok && (detail.Code == "invalid_sources" || detail.Code == "time_evidence_missing") {
				code = detail.Code
			}
			if errors.Is(e, ai.ErrContext) {
				code = "context_too_large"
			}
		} else {
			value, text, e := ai.ResultFromRun(output)
			if e != nil {
				code = "invalid_model_response"
			} else {
				result = &value
				answer = text
			}
		}
	}

	// Completion must outlive the provider timeout, but not an entire server
	// shutdown. Persist failures with a separate bounded context for recovery.
	finish, cancelFinish := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancelFinish()
	telemetry.Content(span, "output.value", answer)
	if code != "" {
		traceErr = errors.New(code)
	}
	finish, persistSpan := telemetry.Start(finish, "chat.save_reply", "CHAIN")
	persistErr := s.store.Complete(finish, run, answer, code, result)
	var saveErr error
	if persistErr != nil {
		saveErr = errors.New("completion failed")
		traceErr = saveErr
		if !errors.Is(persistErr, ai.ErrNotFound) && !errors.Is(persistErr, ai.ErrConflict) {
			log.Printf("chat agent run=%s completion failed", run.ID)
		}
	}
	telemetry.End(persistSpan, saveErr)
	log.Printf("chat agent run=%s trace_id=%s status=%s", run.ID, span.SpanContext().TraceID(), runStatus(code, persistErr))
}

func runStatus(code string, err error) string {
	if code != "" || err != nil {
		return "failed"
	}
	return "succeeded"
}
