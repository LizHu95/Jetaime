package amap

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
	"github.com/LizHu95/Jetaime/services/api/internal/location"
)

type weatherCaller struct {
	calls int
	city  string
}

func (c *weatherCaller) Call(_ context.Context, name string, args any) (json.RawMessage, error) {
	c.calls++
	if name != "maps_weather" {
		return nil, errors.New("unexpected tool")
	}
	raw, _ := json.Marshal(args)
	var weather Weather
	if err := json.Unmarshal(raw, &weather); err != nil {
		return nil, err
	}
	c.city = weather.City
	return json.RawMessage(`{"city":"上海市","forecasts":[{"date":"2026-10-31","dayweather":"晴","nightweather":"多云","daytemp":"20","nighttemp":"15"}]}`), nil
}

func weatherScope() tools.Scope {
	s := scope()
	s.Context.Request.Location = &location.Fix{Longitude: 121.5, Latitude: 31.3, AccuracyMeters: 30, CapturedAt: time.Now(), CoordinateSystem: "gcj02"}
	s.Context.Request.LocationStatus = "available"
	return s
}

func TestWeatherResolvesCurrentAfterAuthorization(t *testing.T) {
	caller := &weatherCaller{}
	authorized, resolutions := false, 0
	resolve := func(_ context.Context, fix location.Fix) (string, error) {
		if !authorized || fix.Coordinates() != "121.500000,31.300000" {
			t.Fatal("unauthorized or incorrect location")
		}
		resolutions++
		return "310110", nil
	}
	registry, err := tools.New(New(caller, func(context.Context, tools.Scope) error { authorized = true; return nil }, resolve)...)
	if err != nil {
		t.Fatal(err)
	}
	s := weatherScope()
	result, err := registry.Execute(context.Background(), s, tools.Call{Name: "maps_weather", Arguments: json.RawMessage(`{"city":"current"}`)})
	if err != nil || caller.calls != 1 || resolutions != 1 || caller.city != "310110" || !strings.Contains(string(result.Data), `"status":"ok"`) {
		t.Fatal("weather current failed", caller, string(result.Data), err)
	}
	contextJSON, _ := json.Marshal(s.Context)
	for _, raw := range []string{string(contextJSON), string(result.Data)} {
		if strings.Contains(raw, "121.5") || strings.Contains(raw, "longitude") {
			t.Fatal("private location exposed", raw)
		}
	}
	_, err = registry.Execute(context.Background(), s, tools.Call{Name: "maps_weather", Arguments: json.RawMessage(`{"city":"北京"}`)})
	if err != nil || caller.calls != 2 || caller.city != "北京" || resolutions != 1 {
		t.Fatal("explicit city overridden", err)
	}
}

func TestWeatherCurrentFailuresDoNotReachWeatherService(t *testing.T) {
	for _, name := range []string{"unauthorized", "missing fix", "different sender", "expired", "wrong coordinates", "missing resolver", "resolver failure", "invalid region", "expires during resolution"} {
		t.Run(name, func(t *testing.T) {
			s := weatherScope()
			caller := &weatherCaller{}
			resolutions := 0
			auth := Authorize(allow)
			resolve := location.RegionResolver(func(context.Context, location.Fix) (string, error) {
				resolutions++
				switch name {
				case "resolver failure":
					return "", errors.New("secret-key private-address 121.500000")
				case "invalid region":
					return "current", nil
				case "expires during resolution":
					s.Context.Request.Location.CapturedAt = time.Now().Add(-6 * time.Minute)
				}
				return "310110", nil
			})
			wantUnavailable := false
			switch name {
			case "unauthorized":
				auth = func(context.Context, tools.Scope) error { return errors.New("denied") }
			case "missing fix":
				s.Context.Request.Location = nil
			case "different sender":
				s.Context.Request.SenderID = "other"
			case "expired":
				s.Context.Request.Location.CapturedAt = time.Now().Add(-6 * time.Minute)
			case "wrong coordinates":
				s.Context.Request.Location.CoordinateSystem = "wgs84"
			case "missing resolver":
				resolve = nil
				wantUnavailable = true
			case "resolver failure", "invalid region":
				wantUnavailable = true
			}
			registry, err := tools.New(New(caller, auth, resolve)...)
			if err != nil {
				t.Fatal(err)
			}
			result, err := registry.Execute(context.Background(), s, tools.Call{Name: "maps_weather", Arguments: json.RawMessage(`{"city":"current"}`)})
			if caller.calls != 0 {
				t.Fatal("invalid current reached weather service")
			}
			if wantUnavailable {
				if err != nil || !strings.Contains(string(result.Data), "城市解析暂不可用") || !strings.Contains(string(result.Data), `"status":"unavailable"`) {
					t.Fatal(string(result.Data), err)
				}
			} else if err == nil {
				t.Fatal("invalid evidence accepted")
			}
			if strings.Contains(string(result.Data), "secret-key") || strings.Contains(string(result.Data), "121.500000") {
				t.Fatal("resolver error leaked")
			}
			if (name == "unauthorized" || name == "missing fix" || name == "different sender" || name == "expired" || name == "wrong coordinates" || name == "missing resolver") && resolutions != 0 {
				t.Fatal("invalid evidence reached resolver")
			}
		})
	}
}
