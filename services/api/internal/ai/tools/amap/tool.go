package amap

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
	"github.com/LizHu95/Jetaime/services/api/internal/location"
)

type Caller interface {
	Call(context.Context, string, any) (json.RawMessage, error)
}
type Authorize func(context.Context, tools.Scope) error

type Search struct {
	ExcludePrevious bool   `json:"excludePrevious,omitempty" jsonschema:"用户要换一批或避免重复推荐时必须为true；后端排除近期已展示地点，本地选项不会发给高德"`
	Keywords        string `json:"keywords" jsonschema:"具体地点名称或餐饮游玩类别；清淡不等于素食"`
	City            string `json:"city" jsonschema:"用户明确提供的城市或行政区；未知时先询问，不猜测当前位置"`
}
type Around struct {
	ExcludePrevious bool   `json:"excludePrevious,omitempty" jsonschema:"用户要换一批或避免重复推荐时必须为true；后端排除近期已展示地点"`
	Keywords        string `json:"keywords"`
	Location        string `json:"location" jsonschema:"高德经纬度，经度在前；使用本次客户端定位时填current，不能编造位置"`
	Radius          int    `json:"radius,omitempty" jsonschema:"半径米，默认3000，上限10000"`
}
type Detail struct {
	ID string `json:"id" jsonschema:"搜索结果中的真实高德地点ID"`
}
type Weather struct {
	City string `json:"city" jsonschema:"城市名称或adcode；查询本轮有效定位所在地时填current，由后端解析；只使用实际返回的预报日期"`
}
type Geo struct {
	Address string `json:"address"`
	City    string `json:"city"`
}
type Direction struct {
	Origin      string `json:"origin" jsonschema:"出发地高德经纬度；使用本次客户端定位时填current，未知先询问"`
	Destination string `json:"destination" jsonschema:"目的地高德经纬度；地点含糊时先澄清"`
}
type Transit struct {
	Origin      string `json:"origin" jsonschema:"出发地高德坐标；本次客户端定位可填current"`
	Destination string `json:"destination"`
	City        string `json:"city" jsonschema:"出发城市"`
	CityD       string `json:"cityd" jsonschema:"到达城市"`
}

// This is a local allowlist, not automatic registration of a remote tool catalog.
func New(caller Caller, authorize Authorize, regionResolver ...location.RegionResolver) []tools.Tool {
	entries := []tools.Tool{
		wrap("maps_text_search", "搜索真实餐厅、景点、公园或具体地点。必须已知城市，城市内最多返回5条；搜索结果可能没有坐标，可再查详情或地理编码。", caller, authorize, func(in Search) (any, error) {
			if !short(in.Keywords, 100) || !short(in.City, 60) {
				return nil, invalid("需要具体搜索关键词和已知城市，请先询问缺失的位置。")
			}
			return struct {
				Keywords  string `json:"keywords"`
				City      string `json:"city"`
				CityLimit bool   `json:"citylimit"`
			}{in.Keywords, in.City, true}, nil
		}),
		wrap("maps_around_search", "按已查询或用户提供的坐标搜索附近餐厅和游玩地点；返回最多5条。本次请求已携带客户端定位时location可填current。", caller, authorize, func(in Around) (any, error) {
			if in.Radius == 0 {
				in.Radius = 3000
			}
			if !short(in.Keywords, 100) || !coordinate(in.Location) || in.Radius < 1 || in.Radius > 10000 {
				return nil, invalid("需要搜索关键词、真实高德坐标（经度,纬度）和1到10000米范围。")
			}
			return struct {
				Keywords string `json:"keywords"`
				Location string `json:"location"`
				Radius   string `json:"radius"`
			}{in.Keywords, in.Location, strconv.Itoa(in.Radius)}, nil
		}),
		wrap("maps_search_detail", "查询搜索结果中地点的坐标、地址、人均、评分和营业时间；缺失字段表示未知，不能补造。", caller, authorize, func(in Detail) (any, error) {
			if !placeID(in.ID) {
				return nil, invalid("使用搜索结果中的单个高德地点ID，不能编造。")
			}
			return in, nil
		}),
		wrap("maps_weather", "查询指定城市天气，最多保留4天。本轮有有效定位且未指定其他地点时city填current，无需追问城市。超出返回日期时说明暂无预报，不能用当前天气代替未来。", caller, authorize, func(in Weather) (any, error) {
			if !short(in.City, 60) {
				return nil, invalid("请提供用户指定的城市或行政区编码。")
			}
			return in, nil
		}, regionResolver...),
		wrap("maps_geo", "将已知城市内的明确地址转换为高德坐标；用于路线起终点。同名地点有歧义时先询问。", caller, authorize, func(in Geo) (any, error) {
			if !short(in.Address, 200) || !short(in.City, 60) {
				return nil, invalid("需要明确的地址和城市，不能猜测出发地。")
			}
			return in, nil
		}),
		wrap("maps_direction_transit_integrated", "查询公交地铁路线，返回最多2个方案、距离米和预计耗时秒，保留上下车站与必要步行信息。", caller, authorize, func(in Transit) (any, error) {
			if !coordinate(in.Origin) || !coordinate(in.Destination) || !short(in.City, 60) || !short(in.CityD, 60) {
				return nil, invalid("需要查询得到的真实起终点高德坐标和各自城市。")
			}
			return in, nil
		}),
	}
	for _, mode := range []string{"walking", "driving", "bicycling"} {
		entries = append(entries, wrap("maps_direction_"+mode, "查询"+map[string]string{"walking": "步行", "driving": "驾车", "bicycling": "骑行"}[mode]+"路线，返回最多2个方案，距离单位米、预计耗时单位秒；需先查询真实起终点坐标。", caller, authorize, func(in Direction) (any, error) {
			if !coordinate(in.Origin) || !coordinate(in.Destination) {
				return nil, invalid("需要查询得到或用户提供的真实高德起终点坐标（经度,纬度）。")
			}
			return in, nil
		}))
	}
	return entries
}

func wrap[I any](name, description string, caller Caller, authorize Authorize, args func(I) (any, error), regionResolver ...location.RegionResolver) tools.Tool {
	return tools.NewTyped(tools.Metadata{Name: name, Description: description, Version: "amap-mcp-v1", Kind: "main", ReadOnly: true, Idempotent: true, Timeout: 30 * time.Second},
		func(ctx context.Context, scope tools.Scope, in I) (Output, error) {
			if authorize == nil || caller == nil {
				return Output{}, errors.New("Amap MCP authorization unavailable")
			}
			if err := authorize(ctx, scope); err != nil {
				return Output{}, err
			}
			resolved, err := resolveCurrent(ctx, scope, in, regionResolver...)
			if err != nil {
				if errors.Is(err, location.ErrRegionUnavailable) {
					return Output{Status: "unavailable", Source: "高德地图 MCP", Message: "当前位置的城市解析暂不可用，请稍后重试或提供城市名称。"}, nil
				}
				return Output{}, err
			}
			request, err := args(resolved)
			if err != nil {
				return Output{}, err
			}
			excludePrevious := false
			switch value := any(in).(type) {
			case Search:
				excludePrevious = value.ExcludePrevious
			case Around:
				excludePrevious = value.ExcludePrevious
			}
			excluded := scope.ExcludedCards
			if excluded == nil {
				excluded = map[string]bool{}
			}
			if excludePrevious {
				for _, place := range scope.Context.RecentPlaces {
					excluded[place.ID] = true
				}
			}
			raw, err := caller.Call(ctx, name, request)
			if err != nil {
				return unavailable(), nil
			}
			out, err := Reduce(name, raw)
			if err != nil {
				return unavailable(), nil
			}
			out = fit(excludePlaces(out, excluded))
			out.Cards = cardCandidates(name, request, out, scope.Context.Request != nil && scope.Context.Request.Location != nil)
			return out, nil
		}, tools.Options[Output]{Present: func(out Output) tools.Completion { return tools.Completion{Cards: out.Cards} }})
}
func unavailable() Output {
	return Output{Status: "unavailable", Source: "高德地图 MCP", Message: "查询未完成或返回格式异常，请稍后重试；不能据此断言没有地点或路线。"}
}
func invalid(message string) error { return tools.InvalidArguments("invalid_amap_arguments", message) }
func short(s string, max int) bool {
	return strings.TrimSpace(s) != "" && len([]rune(s)) <= max && !strings.ContainsRune(s, '\x00')
}
func placeID(id string) bool {
	if len(id) < 1 || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}
func coordinate(s string) bool {
	p := strings.Split(s, ",")
	if len(p) != 2 {
		return false
	}
	lng, e1 := strconv.ParseFloat(p[0], 64)
	lat, e2 := strconv.ParseFloat(p[1], 64)
	return e1 == nil && e2 == nil && lng >= -180 && lng <= 180 && lat >= -90 && lat <= 90
}

// Resolve the sentinel after domain authorization; raw coordinates are never invented by the model.
func resolveCurrent[I any](ctx context.Context, scope tools.Scope, in I, regionResolver ...location.RegionResolver) (I, error) {
	resolve := func(value string) (string, error) {
		if value != "current" {
			return value, nil
		}
		fix, err := currentFix(scope)
		if err != nil {
			return "", err
		}
		return fix.Coordinates(), nil
	}
	var err error
	var value any = in
	switch v := value.(type) {
	case Around:
		v.Location, err = resolve(v.Location)
		value = v
	case Direction:
		v.Origin, err = resolve(v.Origin)
		value = v
	case Transit:
		v.Origin, err = resolve(v.Origin)
		value = v
	case Weather:
		v.City, err = currentCity(ctx, scope, v.City, regionResolver...)
		value = v
	}
	return value.(I), err
}

func currentCity(ctx context.Context, scope tools.Scope, city string, regionResolver ...location.RegionResolver) (string, error) {
	city = strings.TrimSpace(city)
	if city != "current" {
		return city, nil
	}
	fix, err := currentFix(scope)
	if err != nil {
		return "", err
	}
	if len(regionResolver) == 0 || regionResolver[0] == nil {
		return "", location.ErrRegionUnavailable
	}
	code, err := regionResolver[0](ctx, fix)
	if err != nil || !location.ValidAdcode(code) {
		return "", location.ErrRegionUnavailable
	}
	// Resolution must not extend the lifetime of the original fix.
	if _, err := currentFix(scope); err != nil {
		return "", err
	}
	return code, nil
}

func currentFix(scope tools.Scope) (location.Fix, error) {
	r := scope.Context.Request
	if r == nil || r.SenderID != scope.Context.ActorID || r.Location == nil || r.Location.CoordinateSystem != "gcj02" || !r.Location.Valid(time.Now()) {
		return location.Fix{}, invalid("本次客户端定位不可用或已过期，请用户重新定位或提供地点。")
	}
	return *r.Location, nil
}

// Filter both observations and cards, and deduplicate provider POIs within a batch.
func excludePlaces(out Output, excluded map[string]bool) Output {
	places := make([]Place, 0, len(out.Places))
	seen := map[string]bool{}
	removed := 0
	for _, place := range out.Places {
		if excluded["amap:place:"+place.ID] || place.ID != "" && seen[place.ID] {
			removed++
			continue
		}
		seen[place.ID] = true
		places = append(places, place)
	}
	out.Places = places
	if removed > 0 {
		out.Message = "已排除重复或近期已推荐的地点；仅可使用剩余候选。候选不足时调整搜索条件，仍无新结果则如实说明，不用旧地点凑数。"
		if out.Status == "ok" && len(places) == 0 {
			out.Status = "empty"
		}
	}
	return out
}
