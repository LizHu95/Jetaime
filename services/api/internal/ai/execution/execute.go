package execution

import (
	"context"
	"fmt"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/telemetry"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
)

// Service owns one immutable execution entry and is safe for concurrent calls.
type Service struct{ agent ai.Agent }

func newService(agent ai.Agent) (*Service, error) {
	if agent == nil || agent.ID() == "" {
		return nil, fmt.Errorf("agent ID is required")
	}
	return &Service{agent: agent}, nil
}

func (r *Service) Execute(ctx context.Context, in ai.Invocation) (out ai.Run, err error) {
	if err = ctx.Err(); err != nil {
		return
	}
	if in.AgentID == "" {
		in.AgentID = r.agent.ID()
	}
	if in.AgentID != r.agent.ID() {
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
	out, err = r.agent.Invoke(ctx, in)
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
