package search

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/amap"
)

func TestDiscoveryMatching(t *testing.T) {
	definitions := []tools.Definition{}
	for name := range keywords {
		definitions = append(definitions, tools.Definition{Name: name, Description: name})
	}
	for _, tc := range []struct{ query, want string }{
		{"上海今天天气", "maps_weather"}, {"上海找一家清淡餐厅", "maps_text_search"}, {"查询这个地点的营业时间", "maps_search_detail"}, {"从五角场步行去复旦大学", "maps_direction_walking"}, {"附近吃什么", "maps_around_search"}, {"地址转坐标", "maps_geo"}, {"上次约好几点见面 历史约定", "answer_question"}, {"总结昨天聊了什么", "summarize_history"}, {"明天早上九点提醒我喝水", "set_reminder"}, {"怎么去", "maps_direction_walking"}, {"骑车", "maps_direction_bicycling"}, {"maps_weather", "maps_weather"},
	} {
		t.Run(tc.query, func(t *testing.T) {
			matches := Find(definitions, tc.query)
			found := false
			for _, m := range matches {
				found = found || m.Name == tc.want
			}
			if !found || len(matches) > 3 {
				t.Fatalf("%q: %+v", tc.query, matches)
			}
		})
	}
	if matches := Find(definitions, "unrelated_unknown_capability"); len(matches) != 0 {
		t.Fatal(matches)
	}
	// Exact names must not activate their similarly named neighbours.
	if matches := Find(definitions, "maps_weather"); len(matches) != 1 || matches[0].Name != "maps_weather" {
		t.Fatal(matches)
	}
}

func TestSearchValidationAndInvocationIsolation(t *testing.T) {
	entries := amap.New(nil, nil)
	tool := New(entries)
	registry, err := tools.New(append(entries, tool)...)
	if err != nil {
		t.Fatal(err)
	}
	scope := tools.Scope{Context: ai.Context{GroupID: "g", ActorID: "u", Users: []ai.User{{ID: "u", Nickname: "用户"}}}, LoadedTools: map[string]bool{Name: true}}
	invoke := func(query string) error {
		raw, _ := json.Marshal(Input{Query: query})
		_, err := registry.Execute(context.Background(), scope, tools.Call{Name: Name, Arguments: raw})
		return err
	}
	for _, query := range []string{"", "   ", strings.Repeat("字", 201)} {
		if invoke(query) == nil {
			t.Fatal("invalid query accepted")
		}
	}
	if err := invoke("火星数据库删除"); err != nil || len(scope.LoadedTools) != 1 {
		t.Fatal("empty search activated tools", err, scope.LoadedTools)
	}
	if err := invoke("上海天气"); err != nil {
		t.Fatal(err)
	}
	if !scope.LoadedTools["maps_weather"] || len(scope.LoadedTools) != 2 {
		t.Fatal(scope.LoadedTools)
	}
	if err := invoke("上海天气"); err != nil || len(scope.LoadedTools) != 2 {
		t.Fatal("duplicate activation", err)
	}
	other := scope
	other.LoadedTools = map[string]bool{Name: true}
	_, err = registry.Execute(context.Background(), other, tools.Call{Name: "maps_weather", Arguments: json.RawMessage(`{"city":"上海"}`)})
	if e, ok := tools.Repairable(err); !ok || e.Code != "tool_not_loaded" {
		t.Fatal("new invocation inherited loaded tools", err)
	}
}
