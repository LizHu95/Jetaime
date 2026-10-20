// Package bootstrap assembles registered agents and their internal tools.
package bootstrap

import (
	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	mainagent "github.com/LizHu95/Jetaime/services/api/internal/ai/agent/main"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/runtime"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/qa"
)

func New(model llm.ChatModel, name string, tokens int, providers ...qa.ContextProvider) (*ai.Service, error) {
	var provider qa.ContextProvider
	if len(providers) > 0 {
		provider = providers[0]
	}
	return NewWithTools(model, name, tokens, provider)
}

func NewWithTools(model llm.ChatModel, name string, tokens int, provider qa.ContextProvider, extra ...tools.Tool) (*ai.Service, error) {
	main, err := mainagent.NewAgent(model, name, tokens)
	if err != nil {
		return nil, err
	}
	entries := append([]tools.Tool{qa.New(model, name, tokens, provider)}, extra...)
	registry, err := tools.New(entries...)
	if err != nil {
		return nil, err
	}
	main.Tools = registry
	runner, err := runtime.New(main)
	if err != nil {
		return nil, err
	}
	return ai.NewService(runner)
}
