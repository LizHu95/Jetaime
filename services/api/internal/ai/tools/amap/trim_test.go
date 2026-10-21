package amap

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPlacesProjectionAndBudget(t *testing.T) {
	items := []any{}
	for i := 0; i < 30; i++ {
		items = append(items, map[string]any{"id": "B001", "name": strings.Repeat("店", 300), "address": strings.Repeat("路", 300), "type": strings.Repeat("餐饮", 100), "photo": "https://example.com/secret-photo", "metadata": map[string]any{"secret": "must disappear"}, "rating": "4.5", "cost": "50"})
	}
	raw, _ := json.Marshal(map[string]any{"pois": items, "unknown": "must disappear"})
	out, err := Reduce("maps_text_search", raw)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(out)
	if len(out.Places) > 5 || !out.Truncated || len(data) > MaxOutputBytes || !json.Valid(data) || strings.Contains(string(data), "photo") || strings.Contains(string(data), "must disappear") {
		t.Fatal("projection failed", len(data), string(data))
	}
	if len([]rune(out.Places[0].Name)) != 80 || out.Places[0].Rating != "4.5" {
		t.Fatal("essential fields lost")
	}
}

func TestRouteAndWeatherProjection(t *testing.T) {
	for _, test := range []struct{ name, raw string }{
		{"maps_direction_walking", `{"route":{"paths":[{"distance":1000,"duration":600,"polyline":"discard","steps":[{"instruction":"向北走","road":"discard"}]}]}}`},
		{"maps_direction_transit_integrated", `{"distance":"1850","transits":[{"duration":"600","walking_distance":"100","segments":[{"walking":[],"bus":{"buslines":[{"name":"10号线","departure_stop":{"name":"五角场"},"arrival_stop":{"name":"三门路"},"via_stops":[{"name":"discard"}]}]}}]}]}`},
		{"maps_weather", `{"city":"上海","forecasts":[{"date":"2026-10-21","dayweather":"晴","nightweather":"多云","daytemp":"20","nighttemp":"15","unknown":"discard"}]}`},
		{"maps_geo", `{"results":[{"location":"121.5,31.3","city":"上海","district":"杨浦","adcode":"310110","street":[],"number":"discard"}]}`},
		{"maps_search_detail", `{"id":"B001","name":"店","location":"121.5,31.3","rating":[],"cost":"50","opentime2":"10:00-20:00","photo":"discard"}`},
	} {
		out, err := Reduce(test.name, json.RawMessage(test.raw))
		if err != nil {
			t.Fatal(test.name, err)
		}
		data, _ := json.Marshal(out)
		if strings.Contains(string(data), "discard") || len(data) > MaxOutputBytes || out.Status != "ok" {
			t.Fatal(string(data))
		}
		if test.name == "maps_direction_walking" && (out.Routes[0].Distance != "1000" || out.Routes[0].Duration != "600") {
			t.Fatal("numeric values lost")
		}
		if test.name == "maps_direction_transit_integrated" && !strings.Contains(out.Routes[0].Instructions[0], "10号线") {
			t.Fatal("transit stops lost")
		}
	}
}

func TestHardBudgetKeepsValidJSON(t *testing.T) {
	out := Output{Status: "ok", Source: "高德", Places: []Place{{Name: strings.Repeat("<", 20000)}}}
	out = fit(out)
	data, _ := json.Marshal(out)
	if !out.Truncated || len(data) > MaxOutputBytes || !json.Valid(data) {
		t.Fatal("hard output budget violated")
	}
}

func TestMalformedResultsAreNotEmptySearches(t *testing.T) {
	for _, raw := range []string{`{}`, `{"pois":"bad"}`, `{"pois":[null]}`, `{"pois":["bad"]}`, `{"error":"secret"}`} {
		if _, err := Reduce("maps_text_search", json.RawMessage(raw)); err == nil {
			t.Fatal("invalid result accepted", raw)
		}
	}
	if out, err := Reduce("maps_text_search", json.RawMessage(`{"pois":[]}`)); err != nil || out.Status != "empty" {
		t.Fatal(out, err)
	}
	if _, err := Reduce("maps_schema_take_taxi", json.RawMessage(`{}`)); err == nil {
		t.Fatal("unapproved tool allowed")
	}
}
