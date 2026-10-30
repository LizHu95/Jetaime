package mainagent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/agent/main/prompts"
	aicontext "github.com/LizHu95/Jetaime/services/api/internal/ai/context"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/search"
)

// These ten fixed cases are shared with B. This measures request sizes, NOT
// actual tokenizer usage, model recall, task success or real endpoint latency.
func TestDiscoveryMeasurement(t *testing.T) {
	model := &proactiveModel{}
	entries := discoveryEntries(model, &discoveryCaller{})
	base, err := tools.New(entries...)
	if err != nil {
		t.Fatal(err)
	}
	all := base.Definitions()
	initial := []tools.Definition{}
	for _, d := range all {
		if d.Name == "respond" || d.Name == "read_recent_messages" {
			initial = append(initial, d)
		}
	}
	initial = append(initial, search.New(entries).Definition())
	cases := []struct{ id, question, query string }{
		{"normal", "你好", ""},
		{"history", "上次约好几点见面", "上次约好几点见面"},
		{"summary", "总结昨天聊了什么", "总结昨天聊了什么"},
		{"reminder", "明天早上九点提醒我喝水", "明天早上九点提醒我喝水"},
		{"weather", "上海今天天气", "上海今天天气"},
		{"restaurant", "上海找一家清淡餐厅", "上海找一家清淡餐厅"},
		{"detail", "查询这个地点的营业时间", "查询这个地点的营业时间"},
		{"route", "从五角场步行去复旦大学", "从五角场步行去复旦大学"},
		{"missing_location", "附近吃什么", ""},
		{"chain", "找上海餐厅，查详情并规划步行路线", "搜索餐厅"},
	}
	input := testContext()
	at := input.Messages[1].SentAt
	input.Request = &ai.RequestContext{MessageID: "m6", SenderID: input.ActorID, SentAt: at, CurrentTime: at, Timezone: "Asia/Shanghai", TriggerMode: "reactive"}
	input, err = aicontext.ExecutionContext(input)
	if err != nil {
		t.Fatal(err)
	}
	t.Log("case,A_first_upper_bound,C_first_upper_bound,expected_search_calls,C_matches")
	for _, tc := range cases {
		a, err := prompts.MainRequest("test", input, tc.question, 65536, all...)
		if err != nil {
			t.Fatal(err)
		}
		c, err := prompts.MainRequest("test", input, tc.question, 65536, initial...)
		if err != nil {
			t.Fatal(err)
		}
		calls := 0
		matches := []string{}
		if tc.query != "" {
			calls = 1
			for _, m := range search.Find(all, tc.query) {
				matches = append(matches, m.Name)
			}
		}
		if tc.id == "chain" {
			calls = 3
		}
		t.Logf("%s,%d,%d,%d,%v", tc.id, llm.InputTokenUpperBound(a), llm.InputTokenUpperBound(c), calls, matches)
		if llm.InputTokenUpperBound(c) >= llm.InputTokenUpperBound(a) {
			t.Fatal("initial request did not shrink")
		}
	}
	// Same execution path and provider results; only the three explicit discovery
	// decisions differ. Both models are scripted, so this is no quality evaluation.
	business := []string{
		`{"name":"maps_text_search","arguments":{"keywords":"清淡餐厅","city":"上海"}}`,
		`{"name":"maps_search_detail","arguments":{"id":"B001"}}`,
		`{"name":"maps_direction_walking","arguments":{"origin":"121.5,31.3","destination":"121.51,31.31"}}`,
		discoveryAnswer,
	}
	for _, mode := range []string{"A", "C"} {
		script := []string{}
		names := []string{"maps_text_search", "maps_search_detail", "maps_direction_walking"}
		for i, call := range business {
			if mode == "C" && i < len(names) {
				raw, _ := json.Marshal(map[string]any{"name": "tool_search", "arguments": map[string]string{"query": names[i]}})
				script = append(script, string(raw))
			}
			script = append(script, call)
		}
		fake := &proactiveModel{outputs: script}
		caller := &discoveryCaller{}
		var agent *Agent
		if mode == "C" {
			agent = discoveryAgent(t, fake, caller)
		} else {
			registry, e := tools.New(discoveryEntries(fake, caller)...)
			if e != nil {
				t.Fatal(e)
			}
			// B auto-registers discovery in NewAgent; construct the original baseline
			// explicitly so A remains the same full-catalog implementation in both runs.
			agent = &Agent{ChatModel: fake, DefaultModel: "test", Tokens: 65536, Tools: registry}
		}
		run, err := agent.ExecuteMain(context.Background(), input, cases[9].question, nil)
		if err != nil || run.Answer == nil || caller.calls != 3 {
			t.Fatalf("%s scripted chain failed: %v", mode, err)
		}
		cumulative := 0
		for _, req := range fake.requests {
			cumulative += llm.InputTokenUpperBound(req)
		}
		t.Logf("scripted_chain,%s,rounds=%d,first_upper_bound=%d,cumulative_upper_bound=%d,external_calls=%d", mode, len(fake.requests), llm.InputTokenUpperBound(fake.requests[0]), cumulative, caller.calls)
	}
}
