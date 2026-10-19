// Package runtime dispatches registered agents without importing their implementations.
package runtime

import (
	"context"
	"fmt"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/telemetry"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
)

// Runtime is immutable after construction and safe for concurrent requests.
type Runtime struct{ agents map[string]ai.Agent }

func New(agents ...ai.Agent) (*Runtime, error) {
	r := &Runtime{agents: map[string]ai.Agent{}}
	for _, agent := range agents {
		if agent == nil || agent.ID() == "" {
			return nil, fmt.Errorf("agent ID is required")
		}
		if _, exists := r.agents[agent.ID()]; exists {
			return nil, fmt.Errorf("duplicate agent %q", agent.ID())
		}
		r.agents[agent.ID()] = agent
	}
	return r, nil
}

func (r *Runtime) Execute(ctx context.Context, in ai.Invocation) (out ai.Run, err error) {
	if err = ctx.Err(); err != nil {
		return
	}
	agent, ok := r.agents[in.AgentID]
	if !ok {
		return out, fmt.Errorf("agent %q is unavailable", in.AgentID)
	}
	if err = ai.ValidateContext(in.Context); err != nil {
		return
	}
	started := time.Now()
	id := uuid.NewString()
	ctx, span := telemetry.Start(ctx, "ai.invoke", "CHAIN", attribute.String("agent.id", in.AgentID), attribute.String("agent.invocation_id", id), attribute.String("session.id", in.Context.GroupID), attribute.String("user.id", in.Context.ActorID))
	defer func() {
		var traceErr error
		if err != nil {
			traceErr = fmt.Errorf("agent execution failed")
		}
		telemetry.End(span, traceErr)
	}()
	out, err = agent.Invoke(ctx, in)
	out.InvocationID = id
	out.AgentID = in.AgentID
	// Do not replace capability IDs: suggestion confirmation keys refer to them.
	if out.ID == "" {
		out.ID = "run-" + id
		out.GroupID = in.Context.GroupID
		out.ActorID = in.Context.ActorID
		out.StartedAt = started
		out.DurationMS = time.Since(started).Milliseconds()
	}
	if err != nil {
		out.Status = "failed"
		out.Error = err.Error()
	} else if out.Status == "" {
		out.Status = "succeeded"
	}
	return
}
