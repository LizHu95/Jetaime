// Package bootstrap assembles the currently available AI agents.
package bootstrap

import (
	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	mainagent "github.com/LizHu95/Jetaime/services/api/internal/ai/agent/main"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/runtime"
	"github.com/LizHu95/Jetaime/services/api/internal/llm"
)

// NewAI currently registers exactly one agent. Extraction remains a main capability.
func New(model llm.ChatModel, name string, tokens int) (*ai.Service, error) {
	main, err := mainagent.NewAgent(model, name, tokens)
	if err != nil {
		return nil, err
	}
	runner, err := runtime.New(main)
	if err != nil {
		return nil, err
	}
	return ai.NewService(runner)
}
