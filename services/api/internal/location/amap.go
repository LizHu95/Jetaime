package location

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Converter func(context.Context, Fix) (Fix, error)

// AmapConverter keeps the Web service key on the server. Searches and routes use MCP.
func AmapConverter(key string) Converter {
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	return amapConverter(key, client, "https://restapi.amap.com/v3/assistant/coordinate/convert")
}
func amapConverter(key string, client *http.Client, endpoint string) Converter {
	return func(ctx context.Context, fix Fix) (Fix, error) {
		failed := errors.New("location conversion unavailable")
		if !fix.Valid(time.Now()) {
			return Fix{}, failed
		}
		if fix.CoordinateSystem == "gcj02" {
			return fix, nil
		}
		if strings.TrimSpace(key) == "" {
			return Fix{}, failed
		}
		query := url.Values{"key": {key}, "locations": {fix.Coordinates()}, "coordsys": {"gps"}, "output": {"JSON"}}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+query.Encode(), nil)
		if err != nil {
			return Fix{}, failed
		}
		resp, err := client.Do(req)
		if err != nil {
			return Fix{}, failed
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return Fix{}, failed
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 8193))
		if err != nil || len(raw) > 8192 {
			return Fix{}, failed
		}
		var result struct {
			Status    string `json:"status"`
			Locations string `json:"locations"`
		}
		if json.Unmarshal(raw, &result) != nil || result.Status != "1" {
			return Fix{}, failed
		}
		parts := strings.Split(result.Locations, ",")
		if len(parts) != 2 {
			return Fix{}, failed
		}
		lng, e1 := strconv.ParseFloat(parts[0], 64)
		lat, e2 := strconv.ParseFloat(parts[1], 64)
		fix.Longitude, fix.Latitude, fix.CoordinateSystem = lng, lat, "gcj02"
		if e1 != nil || e2 != nil || !fix.Valid(time.Now()) {
			return Fix{}, failed
		}
		return fix, nil
	}
}
