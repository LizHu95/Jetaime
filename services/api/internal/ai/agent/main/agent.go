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

// NewAgent requires the complete tool registry so successful construction yields
// an agent ready to execute; tool selection remains owned by the application.
func NewAgent(model llm.ChatModel, name string, tokens int, registry *tools.Registry) (*Agent, error) {
	if model == nil || strings.TrimSpace(name) == "" || tokens < 4096 {
		return nil, fmt.Errorf("agent requires model and context >=4096")
	}
	if registry == nil {
		return nil, fmt.Errorf("tool registry is required")
	}
	return &Agent{ChatModel: model, DefaultModel: name, Tokens: tokens, Tools: registry}, nil
}
func (a *Agent) ID() string { return "main" }

func (a *Agent) Invoke(ctx context.Context, input ai.Invocation) (ai.Run, error) {
	if input.Mode != "" && input.Mode != "auto" && input.Mode != "main" {
		return ai.Run{}, fmt.Errorf("unsupported agent mode %q", input.Mode)
	}
	return a.ExecuteMain(ctx, input.Context, input.Question, input.LoadExtraction, input.ContextEndSeq)
}
