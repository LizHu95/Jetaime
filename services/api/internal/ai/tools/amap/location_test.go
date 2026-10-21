package amap

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
	"github.com/LizHu95/Jetaime/services/api/internal/location"
)

func TestCurrentLocationResolution(t *testing.T) {
	scope := tools.Scope{Context: ai.Context{ActorID: "alice", Request: &ai.RequestContext{SenderID: "alice", Location: &location.Fix{Longitude: 121.5, Latitude: 31.3, AccuracyMeters: 30, CapturedAt: time.Now(), CoordinateSystem: "gcj02"}}}}
	around, err := resolveCurrent(scope, Around{Keywords: "粥", Location: "current"})
	if err != nil || around.Location != "121.500000,31.300000" {
		t.Fatal(around, err)
	}
	route, err := resolveCurrent(scope, Direction{Origin: "current", Destination: "121.6,31.4"})
	if err != nil || route.Origin != around.Location || route.Destination != "121.6,31.4" {
		t.Fatal(route, err)
	}
	explicit, err := resolveCurrent(scope, Direction{Origin: "120,30", Destination: "121,31"})
	if err != nil || explicit.Origin != "120,30" {
		t.Fatal("explicit origin overwritten")
	}
	scope.Context.Request.SenderID = "bob"
	if _, err = resolveCurrent(scope, Around{Location: "current"}); err == nil {
		t.Fatal("another sender's location accepted")
	}
	scope.Context.Request.SenderID = "alice"
	scope.Context.Request.Location.CapturedAt = time.Now().Add(-6 * time.Minute)
	if _, err = resolveCurrent(scope, Transit{Origin: "current"}); err == nil {
		t.Fatal("stale location accepted")
	}
}

func TestPrivateOriginNotSharedInCardURL(t *testing.T) {
	cards := cardCandidates("maps_direction_walking", Direction{Origin: "121.500000,31.300000", Destination: "121.6,31.4"}, Output{Status: "ok", Routes: []Route{{Distance: "1000", Duration: "600"}}}, true)
	if len(cards) != 1 || strings.Contains(cards[0].Route.MapURL, "121.500000") || strings.Contains(cards[0].Route.MapURL, "from=") {
		t.Fatal("private origin in shared map link")
	}
	if !strings.Contains(cards[0].Route.MapURL, "/marker?") {
		t.Fatal("missing destination link")
	}
}

func TestLocationStaysPrivateThroughToolExecution(t *testing.T) {
	caller := &fakeCaller{}
	registry, err := tools.New(New(caller, allow)...)
	if err != nil {
		t.Fatal(err)
	}
	s := scope()
	s.Context.Request.Location = &location.Fix{Longitude: 121.5, Latitude: 31.3, AccuracyMeters: 30, CapturedAt: time.Now(), CoordinateSystem: "gcj02"}
	s.Context.Request.LocationStatus = "available"
	raw, _ := json.Marshal(s.Context)
	if strings.Contains(string(raw), "longitude") || strings.Contains(string(raw), "121.5") || !strings.Contains(string(raw), `"locationStatus":"available"`) {
		t.Fatal("model context exposed raw position or omitted availability")
	}
	result, err := registry.Execute(context.Background(), s, tools.Call{Name: "maps_direction_walking", Arguments: json.RawMessage(`{"origin":"current","destination":"121.6,31.4"}`)})
	if err != nil || len(result.Completion.Cards) != 1 {
		t.Fatal("current location tool failed", err)
	}
	if !strings.Contains(string(caller.args), `"origin":"121.500000,31.300000"`) {
		t.Fatal("backend did not substitute position")
	}
	raw, _ = json.Marshal(result.Completion.Cards)
	if strings.Contains(string(raw), "121.500000") {
		t.Fatal("position leaked in card")
	}
}
