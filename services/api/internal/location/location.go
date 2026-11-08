// Package location validates short-lived client positions and adapts map coordinates.
package location

import (
	"math"
	"strconv"
	"time"
)

// MaxAge bounds evidence accepted by an in-flight request, not the browser cache.
const MaxAge = 5 * time.Minute

// Retention bounds private database storage from the moment the message is received.
const Retention = 5 * time.Minute

// Fix is client-supplied evidence, not an independently verified device position.
type Fix struct {
	Longitude        float64   `json:"longitude"`
	Latitude         float64   `json:"latitude"`
	AccuracyMeters   float64   `json:"accuracyMeters"`
	CapturedAt       time.Time `json:"capturedAt"`
	CoordinateSystem string    `json:"coordinateSystem"`
}

func (f Fix) Valid(now time.Time) bool {
	return finite(f.Longitude) && finite(f.Latitude) && finite(f.AccuracyMeters) &&
		f.Longitude >= -180 && f.Longitude <= 180 && f.Latitude >= -90 && f.Latitude <= 90 &&
		f.AccuracyMeters > 0 && f.AccuracyMeters <= 1000 &&
		!f.CapturedAt.IsZero() && now.Sub(f.CapturedAt) <= MaxAge && f.CapturedAt.Sub(now) <= 30*time.Second &&
		(f.CoordinateSystem == "wgs84" || f.CoordinateSystem == "gcj02")
}
func (f Fix) Coordinates() string {
	return strconv.FormatFloat(f.Longitude, 'f', 6, 64) + "," + strconv.FormatFloat(f.Latitude, 'f', 6, 64)
}
func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
