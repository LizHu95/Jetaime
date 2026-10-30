package mainagent

import (
	"context"
	"encoding/json"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/amap"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/history"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/qa"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/recent"
	remindertool "github.com/LizHu95/Jetaime/services/api/internal/ai/tools/reminder"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/respond"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/search"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/silence"
	"strings"
	"testing"
)

type discoveryCaller struct{ calls int }

func (c *discoveryCaller) Call(_ context.Context, name string, _ any) (json.RawMessage, error) {
	c.calls++
	if strings.HasPrefix(name, "maps_direction_") {
		return json.RawMessage(`{"route":{"paths":[{"distance":"1000","duration":"600","steps":[]}]}}`), nil
	}
	return json.RawMessage(`{"pois":[{"id":"B001","name":"餐厅","location":"121.5,31.3","address":"大学路"}]}`), nil
}
func discoveryEntries(model llm.ChatModel, caller amap.Caller) []tools.Tool {
	entries := []tools.Tool{respond.New(), silence.New(), qa.New(model, "test", 65536, nil), recent.New(nil), history.New(model, "test", 65536, nil), remindertool.New(nil, nil)}
	return append(entries, amap.New(caller, func(context.Context, tools.Scope) error { return nil })...)
}
func discoveryAgent(t *testing.T, model llm.ChatModel, caller amap.Caller) *Agent {
	t.Helper()
	entries := discoveryEntries(model, caller)
	entries = append(entries, search.New(registryDefinitions(entries)))
	registry, err := tools.New(entries...)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := NewAgent(model, "test", 65536, registry)
	if err != nil {
		t.Fatal(err)
	}
	return agent
}

func registryDefinitions(entries []tools.Tool) []tools.Definition {
	out := []tools.Definition{}
	for _, entry := range entries {
		out = append(out, entry.Definition())
	}
	return out
}
