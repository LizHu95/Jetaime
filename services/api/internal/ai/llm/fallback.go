package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// The request keeps its startup model name. Only the provider request is
// rewritten; callers continue to share the same logical model configuration.
func (a *Adapter) callWithFallback(ctx context.Context, in Request) (Result, error) {
	if in.Model != a.config.Model {
		return Result{}, fmt.Errorf("model must match the configured primary model")
	}
	if in.Stream || in.Think {
		return Result{}, fmt.Errorf("workflow does not support streaming or per-request thinking overrides")
	}
	if !json.Valid(in.Format) {
		return Result{}, fmt.Errorf("valid output schema is required")
	}
	ctx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()
	deadline, _ := ctx.Deadline()
	attemptTimeout := time.Until(deadline) / time.Duration(len(a.candidates))
	start := int(a.active.Load())
	var failures []error
	for offset := range a.candidates {
		if err := ctx.Err(); err != nil {
			return Result{}, fmt.Errorf("model call interrupted: %w", err)
		}
		index := (start + offset) % len(a.candidates)
		candidate := a.candidates[index]
		request := in
		request.Model = candidate.config.Model
		request.Options.NumCtx = min(in.Options.NumCtx, candidate.config.ContextTokens)
		attemptCtx, attemptCancel := context.WithTimeout(ctx, attemptTimeout)
		out, err := candidate.callOnce(attemptCtx, request)
		attemptCancel()
		if err == nil {
			a.active.Store(uint64(index))
			return out, nil
		}
		// Errors from the adapter are sanitized, never provider response bodies.
		failures = append(failures, fmt.Errorf("candidate %d: %w", index+1, err))
		if !errors.Is(err, ErrContextLimit) {
			slog.WarnContext(ctx, "model candidate failed", "profile", candidate.config.Profile, "model", candidate.config.Model, "error", err)
		}
	}
	return Result{}, fmt.Errorf("all model candidates failed: %w", errors.Join(failures...))
}
