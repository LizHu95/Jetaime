package mainagent

import (
	"context"
	"fmt"
	"strings"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/llm"
)

type Agent struct {
	ChatModel    llm.ChatModel
	DefaultModel string
	Tokens       int
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
		return a.ExecuteMain(ctx, input.Context, input.Question, input.LoadExtraction)
	}
	return a.Execute(ctx, input.Context, input.Mode, input.Question)
}
func (a *Agent) Execute(ctx context.Context, input ai.Context, kind, question string) (ai.Run, error) {
	if kind == "" || kind == "auto" || kind == "main" {
		return a.ExecuteMain(ctx, input, question, func(context.Context) (ai.Context, error) { return input, nil })
	}
	if err := ai.ValidateContext(input); err != nil {
		return ai.Run{}, err
	}
	return a.execute(ctx, input, kind, question, a.DefaultModel)
}
