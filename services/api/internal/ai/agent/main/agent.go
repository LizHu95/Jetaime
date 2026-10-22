package mainagent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
	"github.com/google/uuid"
)

type Agent struct {
	ChatModel    llm.ChatModel
	DefaultModel string
	Tokens       int
	Tools        *tools.Registry
}

func NewAgent(model llm.ChatModel, name string, tokens int) (*Agent, error) {
	if model == nil || strings.TrimSpace(name) == "" || tokens < 4096 {
		return nil, fmt.Errorf("agent requires model and context >=4096")
	}
	return &Agent{ChatModel: model, DefaultModel: name, Tokens: tokens}, nil
}
func (a *Agent) ID() string { return "main" }

func (a *Agent) Invoke(ctx context.Context, input ai.Invocation) (ai.Run, error) {
	if input.Mode == "" || input.Mode == "auto" || input.Mode == "main" {
		return a.ExecuteMain(ctx, input.Context, input.Question, input.LoadExtraction, input.ContextEndSeq)
	}
	if err := ai.ValidateContext(input.Context); err != nil {
		return ai.Run{}, err
	}
	if a.Tools == nil {
		return ai.Run{}, fmt.Errorf("tool registry is unavailable")
	}
	result, err := a.Tools.ExecuteLegacy(ctx, tools.Scope{Context: input.Context, ContextEndSeq: input.ContextEndSeq, Question: input.Question, LegacyMode: input.Mode})
	run := ai.Run{ID: "run-" + uuid.NewString(), Kind: input.Mode, GroupID: input.Context.GroupID, ActorID: input.Context.ActorID, Question: input.Question, Model: a.DefaultModel, StartedAt: result.Execution.StartedAt, DurationMS: result.Execution.DurationMS, Steps: result.Execution.Steps, Status: "succeeded"}
	if run.StartedAt.IsZero() {
		run.StartedAt = time.Now()
	}
	applyToolResult(&run, result)
	run.Kind = input.Mode
	if err != nil {
		run.Status = "failed"
		run.Error = err.Error()
	}
	return run, err
}
