package amap

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/mcpclient"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
	"github.com/LizHu95/Jetaime/services/api/internal/location"
)

func TestLiveWeatherCurrent(t *testing.T) {
	if os.Getenv("JETAIME_GAODE_LIVE_TEST") != "true" {
		t.Skip("opt-in live current weather check")
	}
	key := os.Getenv("JETAIME_GAODE_API_KEY")
	if key == "" {
		t.Fatal("missing Amap key")
	}
	client := mcpclient.New("https://mcp.amap.com/mcp?" + url.Values{"key": {key}}.Encode())
	registry, err := tools.New(New(client, allow, location.AmapRegionResolver(key))...)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// Public Shanghai reference point; never uses a person's stored location.
	result, err := registry.Execute(ctx, weatherScope(), tools.Call{Name: "maps_weather", Arguments: json.RawMessage(`{"city":"current"}`)})
	if err != nil {
		t.Fatal("current weather query failed")
	}
	var out Output
	if json.Unmarshal(result.Data, &out) != nil || out.Status != "ok" || len(out.Forecasts) == 0 || len(result.Data) > MaxOutputBytes {
		t.Fatalf("current weather returned status=%s forecasts=%d", out.Status, len(out.Forecasts))
	}
	if strings.Contains(string(result.Data), "121.5") || strings.Contains(string(result.Data), "longitude") {
		t.Fatal("private coordinates in observation")
	}
}

func TestLiveMCP(t *testing.T) {
	if os.Getenv("JETAIME_GAODE_LIVE_TEST") != "true" {
		t.Skip("set JETAIME_GAODE_LIVE_TEST=true and JETAIME_GAODE_API_KEY for live MCP checks")
	}
	key := os.Getenv("JETAIME_GAODE_API_KEY")
	if key == "" {
		t.Fatal("missing Amap key")
	}
	client := mcpclient.New("https://mcp.amap.com/mcp?" + url.Values{"key": {key}}.Encode())
	registry, err := tools.New(New(client, allow, location.AmapRegionResolver(key))...)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	callScope := scope()
	call := func(name string, args any) Output {
		t.Helper()
		raw, _ := json.Marshal(args)
		result, err := registry.Execute(ctx, callScope, tools.Call{Name: name, Arguments: raw})
		if err != nil {
			t.Fatal(name, err)
		}
		if err := ai.ValidateCards(result.Completion.Cards); err != nil {
			t.Fatal(name, "invalid presentation cards", err)
		}
		if (name == "maps_text_search" || name == "maps_search_detail" || name == "maps_around_search" || strings.HasPrefix(name, "maps_direction_")) && len(result.Completion.Cards) == 0 {
			t.Fatal(name, "missing presentation cards")
		}
		var out Output
		if json.Unmarshal(result.Data, &out) != nil || out.Status != "ok" || len(result.Data) > MaxOutputBytes {
			t.Fatalf("%s returned status=%s bytes=%d", name, out.Status, len(result.Data))
		}
		t.Logf("%s: %d JSON bytes, truncated=%t", name, len(result.Data), out.Truncated)
		return out
	}
	origin := call("maps_geo", Geo{Address: "五角场地铁站", City: "上海"})
	destination := call("maps_geo", Geo{Address: "三门路地铁站", City: "上海"})
	if len(origin.Points) == 0 || len(destination.Points) == 0 {
		t.Fatal("test landmarks not found")
	}
	o, d := origin.Points[0].Location, destination.Points[0].Location
	places := call("maps_text_search", Search{Keywords: "粥", City: "上海"})
	if len(places.Places) == 0 {
		t.Fatal("test restaurant search empty")
	}
	call("maps_search_detail", Detail{ID: places.Places[0].ID})
	call("maps_around_search", Around{Keywords: "餐厅", Location: o, Radius: 3000})
	call("maps_weather", Weather{City: "上海"})
	for _, mode := range []string{"walking", "driving", "bicycling"} {
		call("maps_direction_"+mode, Direction{Origin: o, Destination: d})
	}
	call("maps_direction_transit_integrated", Transit{Origin: o, Destination: d, City: "上海", CityD: "上海"})
	fix, err := location.AmapConverter(key)(ctx, location.Fix{Longitude: 121.5, Latitude: 31.3, AccuracyMeters: 50, CapturedAt: time.Now(), CoordinateSystem: "wgs84"})
	if err != nil {
		t.Fatal("client position conversion failed")
	}
	callScope.Context.Request.Location = &fix
	callScope.Context.Request.LocationStatus = "available"
	call("maps_weather", Weather{City: "current"})
	call("maps_around_search", Around{Keywords: "餐厅", Location: "current", Radius: 1000})
	call("maps_direction_walking", Direction{Origin: "current", Destination: d})
}
