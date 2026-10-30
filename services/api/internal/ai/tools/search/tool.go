// Package search discovers deferred capabilities without exposing their schemas up front.
package search

import (
	"context"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
)

const Name = "tool_search"

type Input struct {
	Query string `json:"query" jsonschema:"需要的能力或工具名称，例如上海天气、搜索餐厅、地点详情、步行路线、历史约定、总结聊天、设置提醒"`
}
type Match struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}
type Output struct {
	Message string  `json:"message,omitempty"`
	Status  string  `json:"status"`
	Tools   []Match `json:"tools"`
}

// Deferred is an explicit capability policy, independent of registration order.
func Deferred(name string) bool {
	return strings.HasPrefix(name, "maps_") || name == "answer_question" || name == "summarize_history" || name == "set_reminder"
}

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

func New(entries []tools.Tool) tools.Tool {
	definitions := make([]tools.Definition, 0, len(entries))
	for _, entry := range entries {
		definitions = append(definitions, entry.Definition())
	}
	return tools.NewTyped(tools.Metadata{Name: Name, Description: "按需发现并加载工具。可搜索历史群聊问答、按时间总结、提醒，以及高德地点搜索/详情/地址坐标/天气/步行驾车骑行公交路线。填写具体能力；返回工具仅在本次运行后续轮次可调用，可再次搜索补充。", Kind: "main", ReadOnly: true, Idempotent: true},
		func(_ context.Context, scope tools.Scope, in Input) (Output, error) {
			query := strings.TrimSpace(in.Query)
			if query == "" || utf8.RuneCountInString(query) > 200 {
				return Output{}, tools.InvalidArguments("invalid_search_query", "query 必须为1到200字的具体能力或工具名称。")
			}
			if scope.LoadedTools == nil {
				return Output{}, tools.InvalidArguments("search_unavailable", "工具发现仅在主Agent本次运行内可用。")
			}
			matches := Find(definitions, query)
			status := "ok"
			if len(matches) == 0 {
				status = "empty"
			}
			for _, match := range matches {
				scope.LoadedTools[match.Name] = true
			}
			message := "工具已加载，可在后续轮次调用；参数定义见下一轮tools目录。"
			if status == "empty" {
				message = "未找到匹配能力，请改用具体能力词（如天气、地点详情、历史约定、总结聊天或提醒），或如实说明能力边界。"
			}
			return Output{Status: status, Tools: matches, Message: message}, nil
		})
}
