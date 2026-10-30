package mainagent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/respond"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/search"
)

const discoveryAnswer = `{"name":"respond","arguments":{"status":"answered","answer":"查询已完成。来源：高德地图。","sourceMessageIds":[]}}`

func deferredRegistry(t *testing.T, called *int) *tools.Registry {
	t.Helper()
	entries := []tools.Tool{respond.New()}
	for _, name := range []string{"maps_weather", "maps_text_search", "maps_search_detail", "maps_direction_walking"} {
		entries = append(entries, tools.NewTyped(tools.Metadata{Name: name, Description: name, ReadOnly: true}, func(context.Context, tools.Scope, struct{}) (budgetLookupOutput, error) {
			*called++
			return budgetLookupOutput{Value: "真实外部证据"}, nil
		}))
	}
	r, e := tools.New(entries...)
	if e != nil {
		t.Fatal(e)
	}
	r, e = r.With(search.New(r.Definitions()))
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func catalogHas(t *testing.T, req llm.Request, name string) bool {
	t.Helper()
	var payload struct {
		Tools []tools.Definition `json:"tools"`
	}
	if e := json.Unmarshal([]byte(req.Messages[1].Content), &payload); e != nil {
		t.Fatal(e)
	}
	for _, d := range payload.Tools {
		if d.Name == name {
			return true
		}
	}
	return false
}
func TestDiscoveryActivatesOnlyMatchesAndDoesNotLeakAcrossRuns(t *testing.T) {
	calls := 0
	r := deferredRegistry(t, &calls)
	model := &proactiveModel{outputs: []string{
		`{"name":"maps_weather","arguments":{}}`, // registered, but not loaded: must never execute
		`{"name":"tool_search","arguments":{"query":"maps_weather"}}`,
		`{"name":"maps_weather","arguments":{}}`, discoveryAnswer,
	}}
	agent, e := NewAgent(model, "test", 65536, r)
	if e != nil {
		t.Fatal(e)
	}
	run, e := agent.ExecuteMain(context.Background(), testContext(), "上海今天天气", nil)
	if e != nil || run.Answer == nil || calls != 1 {
		t.Fatal(e, calls)
	}
	first := model.requests[0]
	loaded := model.requests[2]
	if catalogHas(t, first, "maps_weather") || !catalogHas(t, first, "tool_search") || strings.Contains(first.Messages[0].Content, "本轮已提供高德MCP") || strings.Contains(string(first.Format), `"maps_weather"`) {
		t.Fatal("deferred schema or rules leaked into first request")
	}
	if !catalogHas(t, loaded, "maps_weather") || catalogHas(t, loaded, "maps_text_search") || !strings.Contains(loaded.Messages[0].Content, "本轮已提供高德MCP") || !strings.Contains(string(loaded.Format), `"maps_weather"`) {
		t.Fatal("weather-only discovery did not activate its schema and safety rules")
	}
	model.outputs = []string{discoveryAnswer}
	model.requests = nil
	if _, e = agent.ExecuteMain(context.Background(), testContext(), "你好", nil); e != nil {
		t.Fatal(e)
	}
	if catalogHas(t, model.requests[0], "maps_weather") {
		t.Fatal("loaded tools leaked into next invocation")
	}
	if len(r.Definitions()) != 6 {
		t.Fatal("shared registry mutated")
	}
}
func TestDiscoveryRepeatsAndAddsCapabilitiesWithinRun(t *testing.T) {
	calls := 0
	model := &proactiveModel{outputs: []string{
		`{"name":"tool_search","arguments":{"query":"maps_text_search"}}`,
		`{"name":"maps_text_search","arguments":{}}`,
		`{"name":"tool_search","arguments":{"query":"maps_search_detail"}}`,
		`{"name":"maps_search_detail","arguments":{}}`,
		`{"name":"tool_search","arguments":{"query":"maps_direction_walking"}}`,
		`{"name":"maps_direction_walking","arguments":{}}`, discoveryAnswer,
	}}
	agent, e := NewAgent(model, "test", 65536, deferredRegistry(t, &calls))
	if e != nil {
		t.Fatal(e)
	}
	run, e := agent.ExecuteMain(context.Background(), testContext(), "找餐厅查详情和路线", nil)
	if e != nil || run.Answer == nil || calls != 3 || len(model.requests) != 7 {
		t.Fatal(e, calls, len(model.requests))
	}
	for _, name := range []string{"maps_text_search", "maps_search_detail", "maps_direction_walking"} {
		if !catalogHas(t, model.requests[5], name) {
			t.Fatal("missing loaded tool", name)
		}
	}
	if catalogHas(t, model.requests[5], "tool_search") {
		t.Fatal("discovery limit did not hide search")
	}
	for _, req := range model.requests {
		if e := llm.CheckContextBudget(req, 65536); e != nil {
			t.Fatal(e)
		}
	}
}
func TestDiscoveryZeroMatchAndDuplicateRemainBounded(t *testing.T) {
	for _, query := range []string{"量子计算", "maps_weather"} {
		calls := 0
		model := &proactiveModel{outputs: []string{}}
		for i := 0; i < 3; i++ {
			raw, _ := json.Marshal(map[string]any{"name": "tool_search", "arguments": map[string]string{"query": query}})
			model.outputs = append(model.outputs, string(raw))
		}
		model.outputs = append(model.outputs, discoveryAnswer)
		agent, _ := NewAgent(model, "test", 65536, deferredRegistry(t, &calls))
		_, e := agent.ExecuteMain(context.Background(), testContext(), "查询", nil)
		if e != nil || calls != 0 {
			t.Fatal(e, calls)
		}
		count := 0
		var payload struct {
			Tools []tools.Definition `json:"tools"`
		}
		json.Unmarshal([]byte(model.requests[3].Messages[1].Content), &payload)
		for _, d := range payload.Tools {
			if d.Name == "maps_weather" {
				count++
			}
		}
		if count > 1 || catalogHas(t, model.requests[3], "tool_search") {
			t.Fatal("duplicate activation or discovery budget bypass")
		}
	}
}

func TestDiscoveryPreservesBusinessBudgetAndFinalRules(t *testing.T) {
	calls := 0
	model := &proactiveModel{outputs: []string{`{"name":"tool_search","arguments":{"query":"maps_weather"}}`}}
	for i := 0; i < MaxMCPToolSteps; i++ {
		model.outputs = append(model.outputs, `{"name":"maps_weather","arguments":{}}`)
	}
	model.outputs = append(model.outputs, discoveryAnswer)
	agent, _ := NewAgent(model, "test", 65536, deferredRegistry(t, &calls))
	run, e := agent.ExecuteMain(context.Background(), testContext(), "上海天气", nil)
	if e != nil || run.Answer == nil || calls != MaxMCPToolSteps || len(model.requests) != MaxMCPToolSteps+2 {
		t.Fatal(e, calls, len(model.requests))
	}
	last := model.requests[len(model.requests)-1]
	if catalogHas(t, last, "tool_search") || catalogHas(t, last, "maps_weather") || !strings.Contains(last.Messages[0].Content, "本轮已提供高德MCP") || !strings.Contains(last.Messages[0].Content, "工具执行额度已用完") {
		t.Fatal("final response lost domain rules or exposed external tools")
	}
}
func TestDiscoveryRechecksExpandedCatalogBeforeProviderCall(t *testing.T) {
	calls := 0
	registry := deferredRegistry(t, &calls)
	probe := &proactiveModel{outputs: []string{`{"name":"tool_search","arguments":{"query":"maps_weather"}}`, discoveryAnswer}}
	agent, _ := NewAgent(probe, "test", 65536, registry)
	if _, e := agent.ExecuteMain(context.Background(), testContext(), "天气", nil); e != nil {
		t.Fatal(e)
	}
	limit := llm.InputTokenUpperBound(probe.requests[0]) + probe.requests[0].Options.NumPredict + 64
	model := &proactiveModel{outputs: []string{`{"name":"tool_search","arguments":{"query":"maps_weather"}}`, discoveryAnswer}}
	agent, _ = NewAgent(model, "test", limit, registry)
	_, e := agent.ExecuteMain(context.Background(), testContext(), "天气", nil)
	if e == nil || len(model.requests) != 1 {
		t.Fatal("expanded catalog bypassed budget before provider call", e, len(model.requests))
	}
}
