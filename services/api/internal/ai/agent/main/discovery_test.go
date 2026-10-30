package mainagent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
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
)

const discoveryAnswer = `{"name":"respond","arguments":{"status":"answered","answer":"查询已完成。来源：高德地图。","sourceMessageIds":[]}}`

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
	entries = append(entries, search.New(entries))
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
func requestNames(t *testing.T, req llm.Request) map[string]bool {
	t.Helper()
	var payload struct {
		Tools []tools.Definition `json:"tools"`
	}
	if err := json.Unmarshal([]byte(req.Messages[1].Content), &payload); err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, d := range payload.Tools {
		names[d.Name] = true
	}
	return names
}
func TestDiscoveryChainAndRunIsolation(t *testing.T) {
	model := &proactiveModel{outputs: []string{
		`{"name":"tool_search","arguments":{"query":"maps_text_search"}}`,
		`{"name":"maps_text_search","arguments":{"keywords":"清淡餐厅","city":"上海"}}`,
		`{"name":"tool_search","arguments":{"query":"maps_search_detail"}}`,
		`{"name":"maps_search_detail","arguments":{"id":"B001"}}`,
		`{"name":"tool_search","arguments":{"query":"maps_direction_walking"}}`,
		`{"name":"maps_direction_walking","arguments":{"origin":"121.5,31.3","destination":"121.51,31.31"}}`, discoveryAnswer, discoveryAnswer,
	}}
	caller := &discoveryCaller{}
	agent := discoveryAgent(t, model, caller)
	run, err := agent.ExecuteMain(context.Background(), testContext(), "找上海餐厅，查详情并规划步行路线", nil)
	if err != nil || run.Answer == nil || caller.calls != 3 {
		t.Fatalf("chain: %v calls=%d", err, caller.calls)
	}
	first := requestNames(t, model.requests[0])
	if len(first) != 3 || !first["tool_search"] || !first["respond"] || !first["read_recent_messages"] {
		t.Fatal(first)
	}
	for i, want := range map[int]string{1: "maps_text_search", 3: "maps_search_detail", 5: "maps_direction_walking"} {
		names := requestNames(t, model.requests[i])
		if !names[want] || !strings.Contains(string(model.requests[i].Format), want) {
			t.Fatal("not activated in catalog/schema", i)
		}
		if !strings.Contains(model.requests[i].Messages[0].Content, "精确坐标由后端保管") {
			t.Fatal("map rules missing")
		}
	}
	if strings.Contains(model.requests[0].Messages[0].Content, "精确坐标由后端保管") {
		t.Fatal("map rules eagerly loaded")
	}
	if requestNames(t, model.requests[6])["maps_weather"] {
		t.Fatal("unrelated map tool loaded")
	}
	_, err = agent.ExecuteMain(context.Background(), testContext(), "你好", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(requestNames(t, model.requests[7])) != 3 {
		t.Fatal("tool activation leaked across invocations")
	}
}
func TestDiscoveryCannotBypassLoadingAndExhaustion(t *testing.T) {
	caller := &discoveryCaller{}
	model := &proactiveModel{outputs: []string{`{"name":"maps_weather","arguments":{"city":"上海"}}`, discoveryAnswer}}
	agent := discoveryAgent(t, model, caller)
	if _, err := agent.ExecuteMain(context.Background(), testContext(), "上海天气", nil); err != nil || caller.calls != 0 {
		t.Fatal("unloaded tool executed", err)
	}
	if !strings.Contains(model.requests[1].Messages[2].Content, "tool_not_loaded") {
		t.Fatal("no repair feedback")
	}
	model.outputs = nil
	model.requests = nil
	for i := 0; i < 7; i++ {
		model.outputs = append(model.outputs, `{"name":"tool_search","arguments":{"query":"unknown_capability"}}`)
	}
	_, err := agent.ExecuteMain(context.Background(), testContext(), "查一个未知能力", nil)
	if err == nil || len(model.requests) != 7 {
		t.Fatalf("unbounded discovery: %v rounds=%d", err, len(model.requests))
	}
	last := requestNames(t, model.requests[6])
	if len(last) != 1 || !last["respond"] {
		t.Fatal("final response gate lost", last)
	}
}

func TestDeferredGeneralCapabilitiesAndWriteAuthorization(t *testing.T) {
	for _, name := range []string{"answer_question", "summarize_history", "set_reminder"} {
		t.Run(name, func(t *testing.T) {
			executions := 0
			business := tools.NewTyped(tools.Metadata{Name: name, Description: "测试业务能力", ReadOnly: name != "set_reminder"}, func(context.Context, tools.Scope, struct{}) (struct {
				OK bool `json:"ok"`
			}, error) { executions++; return struct {
				OK bool `json:"ok"`
			}{true}, nil })
			entries := []tools.Tool{respond.New(), silence.New(), business}
			entries = append(entries, search.New(entries))
			registry, err := tools.New(entries...)
			if err != nil {
				t.Fatal(err)
			}
			model := &proactiveModel{outputs: []string{`{"name":"tool_search","arguments":{"query":"` + name + `"}}`, `{"name":"` + name + `","arguments":{}}`, discoveryAnswer}}
			agent, err := NewAgent(model, "test", 65536, registry)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := agent.ExecuteMain(context.Background(), testContext(), "处理业务", nil); err != nil || executions != 1 {
				t.Fatal("general deferred capability failed", err, executions)
			}
			if requestNames(t, model.requests[0])[name] || !requestNames(t, model.requests[1])[name] {
				t.Fatal("general capability not deferred")
			}
			if name == "set_reminder" {
				input := testContext()
				at := input.Messages[1].SentAt
				input.Request = &ai.RequestContext{MessageID: "m6", SenderID: input.ActorID, SentAt: at, CurrentTime: at, Timezone: "Asia/Shanghai", TriggerMode: "proactive", AllowWrites: false}
				model.requests = nil
				model.outputs = []string{`{"name":"tool_search","arguments":{"query":"set_reminder"}}`, `{"name":"set_reminder","arguments":{}}`, `{"name":"remain_silent","arguments":{"reason":"无需发言"}}`}
				if _, err := agent.ExecuteMain(context.Background(), input, "提醒", nil); err != nil || executions != 1 {
					t.Fatal("discovery bypassed write authorization", err, executions)
				}
				if !strings.Contains(model.requests[2].Messages[2].Content, "explicit_request_required") {
					t.Fatal("write rejection feedback missing")
				}
			}
		})
	}
}
