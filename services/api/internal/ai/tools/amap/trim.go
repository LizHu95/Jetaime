// Package amap exposes selected official Amap MCP tools with local authorization
// and a strict field/count/text/JSON-byte budget before model observations.
package amap

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

const MaxOutputBytes = 8192

type Output struct {
	Cards     []ai.Card  `json:"-"`
	Status    string     `json:"status"`
	Source    string     `json:"source"`
	Truncated bool       `json:"truncated"`
	Message   string     `json:"message,omitempty"`
	City      string     `json:"city,omitempty"`
	Places    []Place    `json:"places,omitempty"`
	Points    []Point    `json:"points,omitempty"`
	Routes    []Route    `json:"routes,omitempty"`
	Forecasts []Forecast `json:"forecasts,omitempty"`
}
type Place struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Address      string `json:"address"`
	Location     string `json:"location,omitempty"`
	City         string `json:"city,omitempty"`
	Category     string `json:"category,omitempty"`
	Rating       string `json:"rating,omitempty"`
	Cost         string `json:"cost,omitempty"`
	OpeningHours string `json:"openingHours,omitempty"`
}
type Point struct {
	Location string `json:"location"`
	City     string `json:"city"`
	District string `json:"district"`
	Adcode   string `json:"adcode"`
}
type Route struct {
	Distance        string   `json:"distanceMeters,omitempty"`
	Duration        string   `json:"durationSeconds,omitempty"`
	WalkingDistance string   `json:"walkingDistanceMeters,omitempty"`
	Instructions    []string `json:"instructions"`
}
type Forecast struct {
	Date         string `json:"date"`
	DayWeather   string `json:"dayWeather"`
	NightWeather string `json:"nightWeather"`
	DayTemp      string `json:"dayTemp"`
	NightTemp    string `json:"nightTemp"`
}

type reducer struct{ truncated bool }

func (r *reducer) text(v any, n int) string {
	var s string
	switch v := v.(type) {
	case string:
		s = v
	case json.Number:
		s = v.String()
	}
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > n {
		s = string([]rune(s)[:n])
		r.truncated = true
	}
	return s
}
func obj(v any) map[string]any { m, _ := v.(map[string]any); return m }

func objectList(data map[string]any, key string) bool {
	items, ok := data[key].([]any)
	if !ok {
		return false
	}
	for _, item := range items {
		if obj(item) == nil {
			return false
		}
	}
	return true
}
func (r *reducer) list(v any, n int) []any {
	a, _ := v.([]any)
	if len(a) > n {
		r.truncated = true
		a = a[:n]
	}
	return a
}

// Reduce reconstructs a fresh result from known fields. No raw maps, pictures,
// polylines, provider messages, URLs or arbitrary nested metadata can pass through.
func Reduce(name string, raw json.RawMessage) (Output, error) {
	var data map[string]any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if d.Decode(&data) != nil || data == nil {
		return Output{}, errors.New("invalid Amap MCP object")
	}
	r := &reducer{}
	out := Output{Status: "ok", Source: "高德地图 MCP"}
	switch name {
	case "maps_text_search", "maps_around_search":
		if !objectList(data, "pois") {
			return Output{}, errors.New("missing places")
		}
		for _, p := range r.list(data["pois"], 5) {
			out.Places = append(out.Places, r.place(obj(p)))
		}
		if len(out.Places) == 0 {
			out.Status = "empty"
		}
	case "maps_search_detail":
		if id, ok := data["id"].(string); !ok || !placeID(id) {
			return Output{}, errors.New("missing place detail")
		}
		out.Places = []Place{r.place(data)}
	case "maps_geo":
		if !objectList(data, "results") {
			return Output{}, errors.New("missing geocodes")
		}
		for _, v := range r.list(data["results"], 3) {
			p := obj(v)
			out.Points = append(out.Points, Point{r.text(p["location"], 40), r.text(p["city"], 60), r.text(p["district"], 60), r.text(p["adcode"], 6)})
		}
		if len(out.Points) == 0 {
			out.Status = "empty"
		}
	case "maps_weather":
		if !objectList(data, "forecasts") {
			return Output{}, errors.New("missing forecasts")
		}
		out.City = r.text(data["city"], 60)
		for _, v := range r.list(data["forecasts"], 4) {
			f := obj(v)
			out.Forecasts = append(out.Forecasts, Forecast{r.text(f["date"], 10), r.text(f["dayweather"], 40), r.text(f["nightweather"], 40), r.text(f["daytemp"], 12), r.text(f["nighttemp"], 12)})
		}
		if len(out.Forecasts) == 0 {
			out.Status = "empty"
		}
	case "maps_direction_walking", "maps_direction_driving", "maps_direction_bicycling", "maps_direction_transit_integrated":
		root := data
		if nested := obj(data["route"]); nested != nil {
			root = nested
		}
		if nested := obj(data["data"]); nested != nil {
			root = nested
		}
		key := "paths"
		if name == "maps_direction_transit_integrated" {
			key = "transits"
		}
		if !objectList(root, key) {
			return Output{}, errors.New("missing routes")
		}
		for _, v := range r.list(root[key], 2) {
			p := obj(v)
			route := Route{Distance: r.number(p["distance"]), Duration: r.number(p["duration"]), WalkingDistance: r.number(p["walking_distance"]), Instructions: []string{}}
			// A transit response may only supply total distance at the root.
			if route.Distance == "" {
				route.Distance = r.number(root["distance"])
			}
			for _, s := range r.list(p["steps"], 8) {
				route.Instructions = append(route.Instructions, r.text(obj(s)["instruction"], 120))
			}
			for _, s := range r.list(p["segments"], 8) {
				segment := obj(s)
				walk := obj(segment["walking"])
				if distance := r.number(walk["distance"]); distance != "" {
					route.Instructions = append(route.Instructions, "步行 "+distance+" 米")
				}
				bus := obj(segment["bus"])
				for _, b := range r.list(bus["buslines"], 1) {
					line := obj(b)
					route.Instructions = append(route.Instructions, r.text(line["name"], 60)+"："+r.text(obj(line["departure_stop"])["name"], 40)+" → "+r.text(obj(line["arrival_stop"])["name"], 40))
				}
			}
			if len(route.Instructions) > 8 {
				r.truncated = true
				route.Instructions = route.Instructions[:8]
			}
			out.Routes = append(out.Routes, route)
		}
		if len(out.Routes) == 0 {
			out.Status = "empty"
		}
	default:
		return Output{}, errors.New("Amap MCP tool not allowed")
	}
	out.Truncated = r.truncated
	return fit(out), nil
}

func (r *reducer) number(v any) string {
	s := r.text(v, 24)
	n, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n > 1e10 {
		return ""
	}
	return s
}
func (r *reducer) place(p map[string]any) Place {
	return Place{
		ID: r.text(p["id"], 64), Name: r.text(p["name"], 80), Address: r.text(p["address"], 120), Location: r.text(p["location"], 40), City: r.text(p["city"], 60), Category: r.text(p["type"], 60), Rating: r.text(p["rating"], 12), Cost: r.text(p["cost"], 16), OpeningHours: r.text(p["opentime2"], 80),
	}
}
func fit(out Output) Output {
	for {
		data, err := json.Marshal(out)
		if err == nil && len(data) <= MaxOutputBytes {
			return out
		}
		out.Truncated = true
		switch {
		case len(out.Places) > 0:
			out.Places = out.Places[:len(out.Places)-1]
		case len(out.Routes) > 0:
			out.Routes = out.Routes[:len(out.Routes)-1]
		case len(out.Points) > 0:
			out.Points = out.Points[:len(out.Points)-1]
		case len(out.Forecasts) > 0:
			out.Forecasts = out.Forecasts[:len(out.Forecasts)-1]
		default:
			return Output{Status: "unavailable", Source: "高德地图 MCP", Truncated: true, Message: "查询结果超过输出预算。"}
		}
	}
}
