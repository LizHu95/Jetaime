package search

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
)

func TestDiscoveryIsBoundedAndMatchesSpecificCapabilities(t *testing.T) {
	names := []string{"maps_text_search", "maps_around_search", "maps_search_detail", "maps_weather", "maps_geo", "maps_direction_walking", "maps_direction_driving", "maps_direction_bicycling", "maps_direction_transit_integrated", "set_reminder"}
	definitions := []tools.Definition{}
	for _, name := range names {
		definitions = append(definitions, tools.Definition{Name: name, Description: name, InputSchema: json.RawMessage(`{"type":"object"}`)})
	}
	for _, tc := range []struct{ query, want string }{
		{"上海天气", "maps_weather"}, {"上海找一家清淡餐厅", "maps_text_search"}, {"查询这个地点的营业时间", "maps_search_detail"}, {"地址坐标", "maps_geo"}, {"步行", "maps_direction_walking"}, {"开车", "maps_direction_driving"}, {"骑车", "maps_direction_bicycling"}, {"地铁", "maps_direction_transit_integrated"}, {"附近", "maps_around_search"}, {"maps_weather", "maps_weather"},
	} {
		got := Find(definitions, tc.query)
		if len(got) == 0 || got[0].Name != tc.want || len(got) > 3 {
			t.Errorf("%q = %v, want first %s", tc.query, got, tc.want)
		}
	}
	if got := Find(definitions, "设置提醒"); len(got) != 0 {
		t.Fatal("B must keep common tools outside discovery", got)
	}
	if got := Find(definitions, "量子计算"); len(got) != 0 {
		t.Fatal("unknown query matched", got)
	}
	registry, err := tools.New(New(definitions))
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"", "   ", strings.Repeat("天", 201)} {
		raw, _ := json.Marshal(Arguments{Query: query})
		if _, err := registry.Execute(context.Background(), tools.Scope{Context: ai.Context{GroupID: "g", ActorID: "u", Users: []ai.User{{ID: "u", Nickname: "用户"}}}}, tools.Call{Name: Name, Arguments: raw}); err == nil {
			t.Fatal("invalid query accepted")
		}
	}
	result, err := registry.Execute(context.Background(), tools.Scope{Context: ai.Context{GroupID: "g", ActorID: "u", Users: []ai.User{{ID: "u", Nickname: "用户"}}}}, tools.Call{Name: Name, Arguments: json.RawMessage(`{"query":"天气"}`)})
	if err != nil || strings.Contains(string(result.Data), "inputSchema") {
		t.Fatal("search must return light metadata only", err, string(result.Data))
	}
	var got Output
	if err := json.Unmarshal(result.Data, &got); err != nil || len(got.Tools) != 1 {
		t.Fatal(err, string(result.Data))
	}
}
