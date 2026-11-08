package location

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestFixValidation(t *testing.T) {
	now := time.Now()
	good := Fix{Longitude: 121.5, Latitude: 31.3, AccuracyMeters: 50, CapturedAt: now, CoordinateSystem: "wgs84"}
	if !good.Valid(now) {
		t.Fatal("valid fix rejected")
	}
	cases := []func(*Fix){
		func(f *Fix) { f.Longitude = 181 }, func(f *Fix) { f.Latitude = math.NaN() },
		func(f *Fix) { f.AccuracyMeters = 1001 }, func(f *Fix) { f.AccuracyMeters = 0 },
		func(f *Fix) { f.CapturedAt = now.Add(-MaxAge - time.Second) }, func(f *Fix) { f.CapturedAt = now.Add(time.Minute) },
		func(f *Fix) { f.CoordinateSystem = "unknown" },
	}
	for i, change := range cases {
		fix := good
		change(&fix)
		if fix.Valid(now) {
			t.Errorf("invalid case %d accepted", i)
		}
	}
}
func TestAmapConversion(t *testing.T) {
	fix := Fix{Longitude: 121.5, Latitude: 31.3, AccuracyMeters: 50, CapturedAt: time.Now(), CoordinateSystem: "wgs84"}
	for _, body := range []string{`{"status":"1","locations":"121.504,31.298"}`, `{"status":"0","info":"secret-key"}`, `{"status":"1","locations":"NaN,31"}`, `{"status":"1","locations":"121,31;122,32"}`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("coordsys") != "gps" || r.URL.Query().Get("locations") != "121.500000,31.300000" {
					t.Error("wrong conversion request")
				}
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()
			got, err := amapConverter("secret-key", server.Client(), server.URL)(context.Background(), fix)
			if body == `{"status":"1","locations":"121.504,31.298"}` {
				if err != nil || got.CoordinateSystem != "gcj02" || got.Longitude != 121.504 || got.AccuracyMeters != 50 || !got.CapturedAt.Equal(fix.CapturedAt) {
					t.Fatalf("conversion: %+v %v", got, err)
				}
			} else if err == nil || err.Error() != "location conversion unavailable" {
				t.Fatalf("unsafe failure: %v", err)
			}
		})
	}
}

func TestLiveAmapConversion(t *testing.T) {
	if os.Getenv("JETAIME_GAODE_LIVE_TEST") != "true" {
		t.Skip("opt-in live conversion check")
	}
	fix := Fix{Longitude: 121.5, Latitude: 31.3, AccuracyMeters: 50, CapturedAt: time.Now(), CoordinateSystem: "wgs84"}
	got, err := AmapConverter(os.Getenv("JETAIME_GAODE_API_KEY"))(context.Background(), fix)
	if err != nil || !got.Valid(time.Now()) || got.CoordinateSystem != "gcj02" {
		t.Fatal("live conversion failed", err)
	}
	if got.Longitude == fix.Longitude && got.Latitude == fix.Latitude {
		t.Fatal("coordinates were not converted")
	}
}
