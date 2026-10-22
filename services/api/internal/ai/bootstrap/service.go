// Package bootstrap assembles agents and their runtime using an explicit tool registry.
package bootstrap

import (
	"fmt"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	mainagent "github.com/LizHu95/Jetaime/services/api/internal/ai/agent/main"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/intervention"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/orchestration"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/runtime"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
)

// New does not select, create or add tools. Each application entry point owns
// its complete capability list and passes an already validated registry here.
func New(model llm.ChatModel, name string, tokens int, registry *tools.Registry, evidence ...ai.InterventionEvidenceProvider) (*ai.Service, error) {
	if registry == nil {
		return nil, fmt.Errorf("tool registry is required")
	}
	main, err := mainagent.NewAgent(model, name, tokens)
	if err != nil {
		return nil, err
	}
	main.Tools = registry
	judge, err := intervention.New(model, name, tokens)
	if err != nil {
		return nil, err
	}
	entry, err := orchestration.New(main, judge, evidence...)
	if err != nil {
		return nil, err
	}
	runner, err := runtime.New(entry)
	if err != nil {
		return nil, err
	}
	return ai.NewService(runner)
}
