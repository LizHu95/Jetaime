package location

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// RegionResolver returns an administrative adcode for a valid GCJ02 fix.
type RegionResolver func(context.Context, Fix) (string, error)

var ErrRegionUnavailable = errors.New("location region resolution unavailable")

// AmapRegionResolver keeps coordinates and the Web service key on the server.
func AmapRegionResolver(key string) RegionResolver {
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	return amapRegionResolver(key, client, "https://restapi.amap.com/v3/geocode/regeo")
}

func amapRegionResolver(key string, client *http.Client, endpoint string) RegionResolver {
	return func(ctx context.Context, fix Fix) (string, error) {
		if !fix.Valid(time.Now()) || fix.CoordinateSystem != "gcj02" || strings.TrimSpace(key) == "" {
			return "", ErrRegionUnavailable
		}
		query := url.Values{"key": {key}, "location": {fix.Coordinates()}, "extensions": {"base"}, "output": {"JSON"}}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+query.Encode(), nil)
		if err != nil {
			return "", ErrRegionUnavailable
		}
		resp, err := client.Do(req)
		if err != nil {
			return "", ErrRegionUnavailable
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return "", ErrRegionUnavailable
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 8193))
		if err != nil || len(raw) > 8192 {
			return "", ErrRegionUnavailable
		}
		var result struct {
			Status    string `json:"status"`
			Regeocode struct {
				AddressComponent struct {
					Adcode string `json:"adcode"`
				} `json:"addressComponent"`
			} `json:"regeocode"`
		}
		if json.Unmarshal(raw, &result) != nil || result.Status != "1" || !ValidAdcode(result.Regeocode.AddressComponent.Adcode) {
			return "", ErrRegionUnavailable
		}
		return result.Regeocode.AddressComponent.Adcode, nil
	}
}

func ValidAdcode(code string) bool {
	if len(code) != 6 || code == "000000" {
		return false
	}
	for _, c := range code {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
