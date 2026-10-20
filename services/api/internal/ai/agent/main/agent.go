package mainagent

import (
	"context"
	"fmt"
	"strings"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
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
	return result.Run, err
}
