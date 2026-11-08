package location

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAmapRegionResolution(t *testing.T) {
	fix := Fix{Longitude: 121.5, Latitude: 31.3, AccuracyMeters: 50, CapturedAt: time.Now(), CoordinateSystem: "gcj02"}
	for _, tc := range []struct {
		name, body string
		status     int
		ok         bool
	}{
		{"district", `{"status":"1","regeocode":{"addressComponent":{"city":"上海市","adcode":"310110"}}}`, 200, true},
		{"municipality empty city", `{"status":"1","regeocode":{"addressComponent":{"city":[],"adcode":"110101"}}}`, 200, true},
		{"provider error", `{"status":"0","info":"secret-key 121.500000"}`, 200, false},
		{"missing region", `{"status":"1","regeocode":{}}`, 200, false},
		{"invalid code", `{"status":"1","regeocode":{"addressComponent":{"adcode":"current"}}}`, 200, false},
		{"empty code", `{"status":"1","regeocode":{"addressComponent":{"adcode":[]}}}`, 200, false},
		{"zero code", `{"status":"1","regeocode":{"addressComponent":{"adcode":"000000"}}}`, 200, false},
		{"http error", "secret-key", 503, false},
		{"oversized", strings.Repeat(" ", 8193), 200, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				q := r.URL.Query()
				if q.Get("location") != fix.Coordinates() || q.Get("key") != "secret-key" || q.Get("extensions") != "base" || q.Get("output") != "JSON" {
					t.Error("wrong region request")
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			code, err := amapRegionResolver("secret-key", server.Client(), server.URL)(context.Background(), fix)
			if tc.ok {
				if err != nil || !ValidAdcode(code) {
					t.Fatal(code, err)
				}
			} else if code != "" || !errors.Is(err, ErrRegionUnavailable) || strings.Contains(err.Error(), "secret-key") {
				t.Fatal("unsafe region failure", code, err)
			}
		})
	}
}

func TestAmapRegionRejectsInvalidFixAndMissingKey(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer server.Close()
	fix := Fix{Longitude: 121.5, Latitude: 31.3, AccuracyMeters: 50, CapturedAt: time.Now(), CoordinateSystem: "gcj02"}
	for _, tc := range []struct {
		key string
		fix Fix
	}{
		{"", fix},
		{"key", func() Fix { f := fix; f.CoordinateSystem = "wgs84"; return f }()},
		{"key", func() Fix { f := fix; f.CapturedAt = time.Now().Add(-MaxAge - time.Second); return f }()},
	} {
		if code, err := amapRegionResolver(tc.key, server.Client(), server.URL)(context.Background(), tc.fix); code != "" || !errors.Is(err, ErrRegionUnavailable) {
			t.Fatal("invalid location accepted", code, err)
		}
	}
	if called {
		t.Fatal("invalid evidence reached provider")
	}
}

func TestAmapRegionCancellationAndRedirect(t *testing.T) {
	targetCalled := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalled = true }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer server.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resolve := amapRegionResolver("secret-key", client, server.URL)
	fix := Fix{Longitude: 121.5, Latitude: 31.3, AccuracyMeters: 50, CapturedAt: time.Now(), CoordinateSystem: "gcj02"}
	if _, err := resolve(context.Background(), fix); !errors.Is(err, ErrRegionUnavailable) || targetCalled {
		t.Fatal("redirect followed or accepted", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if code, err := resolve(ctx, fix); code != "" || !errors.Is(err, ErrRegionUnavailable) {
		t.Fatal("canceled lookup succeeded", code, err)
	}
}
