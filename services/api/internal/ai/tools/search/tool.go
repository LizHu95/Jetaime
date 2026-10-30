// Package search discovers deferred capabilities without exposing their full schemas.
package search

import (
	"context"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
	"github.com/google/jsonschema-go/jsonschema"
)

const Name = "tool_search"

type Arguments struct {
	Query string `json:"query" jsonschema:"要完成的具体任务或工具名称，例如查询上海天气、搜索附近餐厅、查询地点详情、步行路线；只发现工具，不执行业务"`
}
type Match struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}
type Output struct {
	Status string  `json:"status"`
	Tools  []Match `json:"tools"`
}

// Deferred is deliberately explicit: unrelated future tools remain compatible.
func Deferred(name string) bool { return strings.HasPrefix(name, "maps_") }

var keywords = map[string][]string{
	"answer_question":                   {"历史", "上次", "约好", "约定", "之前", "以前", "记得", "谁说", "history", "recall"},
	"summarize_history":                 {"总结", "回顾", "聊了什么", "聊天记录", "汇总", "summary", "summarize"},
	"set_reminder":                      {"提醒", "闹钟", "remind", "reminder"},
	"maps_weather":                      {"天气", "气温", "下雨", "weather"},
	"maps_text_search":                  {"餐厅", "饭店", "咖啡", "景点", "公园", "商家", "搜地点", "找地点", "搜索地点", "吃什么", "去哪吃", "吃饭", "美食", "去哪里玩", "text search", "restaurant"},
	"maps_around_search":                {"附近", "周边", "around", "nearby"},
	"maps_search_detail":                {"详情", "营业", "评分", "人均", "地点坐标", "detail"},
	"maps_geo":                          {"地理编码", "地址转坐标", "地址坐标", "坐标", "geocode", "geocoding"},
	"maps_direction_walking":            {"步行", "走路", "walking", "怎么去", "路线", "导航", "距离", "多远", "多久"},
	"maps_direction_driving":            {"驾车", "开车", "driving", "怎么去", "路线", "导航"},
	"maps_direction_bicycling":          {"骑行", "骑车", "bicycling", "cycling"},
	"maps_direction_transit_integrated": {"公交", "地铁", "公共交通", "transit", "怎么去", "路线", "导航"},
}

func Find(definitions []tools.Definition, query string) []Match {
	query = strings.ToLower(strings.TrimSpace(query))
	type candidate struct {
		match Match
		score int
	}
	var ranked []candidate
	for _, d := range definitions {
		if !Deferred(d.Name) {
			continue
		}
		score := 0
		if query == d.Name {
			score = 1000
		} else {
			for _, keyword := range keywords[d.Name] {
				if strings.Contains(query, keyword) {
					score += 10
				}
			}
			// Match user-provided name fragments and words from descriptions locally.
			for _, word := range strings.Fields(query) {
				if len(word) >= 3 && (strings.Contains(d.Name, word) || strings.Contains(strings.ToLower(d.Description), word)) {
					score++
				}
			}
		}
		if score > 0 {
			ranked = append(ranked, candidate{Match{d.Name, d.Description}, score})
		}
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score == ranked[j].score {
			return ranked[i].match.Name < ranked[j].match.Name
		}
		return ranked[i].score > ranked[j].score
	})
	out := []Match{}
	for _, item := range ranked {
		if len(out) == 3 {
			break
		}
		out = append(out, item.match)
	}
	return out
}

func New(catalog []tools.Definition) *tools.Typed[Arguments, Output] {
	return tools.NewTyped(tools.Metadata{Name: Name, Description: "按需发现并加载高德地图工具：地点搜索、附近商家、地点详情、地址坐标、天气、步行/驾车/骑行/公交路线。涉及这些外部事实时先搜索对应能力，再调用命中的工具；可继续搜索补充能力。只返回本次部署已启用工具。", Kind: "main", ReadOnly: true, Idempotent: true}, func(_ context.Context, _ tools.Scope, in Arguments) (Output, error) {
		q := strings.ToLower(strings.TrimSpace(in.Query))
		if q == "" || utf8.RuneCountInString(q) > 200 {
			return Output{}, tools.InvalidArguments("invalid_search_query", "query需为1至200字的具体任务或工具名。")
		}
		out := Output{Status: "empty", Tools: Find(catalog, q)}
		if len(out.Tools) > 0 {
			out.Status = "ok"
		}
		return out, nil
	}, tools.Options[Output]{Input: func(s *jsonschema.Schema) {
		minimum, maximum := 1, 200
		s.Properties["query"].MinLength = &minimum
		s.Properties["query"].MaxLength = &maximum
	}})
}
