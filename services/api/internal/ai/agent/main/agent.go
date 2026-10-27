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
	if input.Mode != "" && input.Mode != "auto" && input.Mode != "main" {
		return ai.Run{}, fmt.Errorf("unsupported agent mode %q", input.Mode)
	}
	return a.ExecuteMain(ctx, input.Context, input.Question, input.LoadExtraction, input.ContextEndSeq)
}
