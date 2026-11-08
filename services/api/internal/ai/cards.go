package ai

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"strings"
	"unicode/utf8"
)

const MaxCards = 5
const MaxCardReason = 160

// Card facts are assembled from tool evidence. The model can select an ID and
// add a reason, but cannot supply prices, route metrics or external URLs.
type Card struct {
	ID     string     `json:"id"`
	Type   string     `json:"type"`
	Title  string     `json:"title"`
	Source string     `json:"source"`
	Reason string     `json:"reason,omitempty"`
	Place  *PlaceCard `json:"place,omitempty"`
	Route  *RouteCard `json:"route,omitempty"`
}
type PlaceCard struct {
	Name          string   `json:"name"`
	Address       string   `json:"address"`
	City          string   `json:"city,omitempty"`
	CostPerPerson *float64 `json:"costPerPerson"`
	Rating        *float64 `json:"rating"`
	OpeningHours  string   `json:"openingHours,omitempty"`
	MapURL        string   `json:"mapUrl"`
}
type RouteCard struct {
	Mode                  string   `json:"mode"`
	DistanceMeters        *float64 `json:"distanceMeters"`
	DurationSeconds       *float64 `json:"durationSeconds"`
	WalkingDistanceMeters *float64 `json:"walkingDistanceMeters"`
	Instructions          []string `json:"instructions"`
	MapURL                string   `json:"mapUrl"`
}

func ValidateCards(cards []Card) error {
	if len(cards) > MaxCards {
		return fmt.Errorf("too many cards")
	}
	seen := map[string]bool{}
	for _, card := range cards {
		if card.ID == "" || len(card.ID) > 100 || seen[card.ID] || card.Source != "amap" || strings.TrimSpace(card.Title) == "" || utf8.RuneCountInString(card.Title) > 80 || utf8.RuneCountInString(card.Reason) > MaxCardReason || strings.ContainsRune(card.Reason, '\x00') {
			return fmt.Errorf("invalid card identity or reason")
		}
		seen[card.ID] = true
		switch card.Type {
		case "place":
			p := card.Place
			if p == nil || card.Route != nil || p.Name == "" || !validMapURL(p.MapURL) || !validMetric(p.CostPerPerson) || !validMetric(p.Rating) || (p.Rating != nil && *p.Rating > 5) {
				return fmt.Errorf("invalid place card")
			}
		case "route":
			r := card.Route
			if r == nil || card.Place != nil || !validMapURL(r.MapURL) || !validMetric(r.DistanceMeters) || !validMetric(r.DurationSeconds) || !validMetric(r.WalkingDistanceMeters) || len(r.Instructions) > 8 {
				return fmt.Errorf("invalid route card")
			}
			if r.Mode != "walking" && r.Mode != "driving" && r.Mode != "bicycling" && r.Mode != "transit" {
				return fmt.Errorf("invalid route mode")
			}
		default:
			return fmt.Errorf("unsupported card type")
		}
	}
	data, err := json.Marshal(cards)
	if err != nil || len(data) > 12*1024 {
		return fmt.Errorf("card output exceeds budget")
	}
	return nil
}
func validMetric(value *float64) bool {
	return value == nil || !math.IsNaN(*value) && !math.IsInf(*value, 0) && *value >= 0 && *value <= 1e10
}
func validMapURL(value string) bool {
	u, err := url.Parse(value)
	return err == nil && u.Scheme == "https" && u.Host == "uri.amap.com" && u.User == nil && (u.Path == "/marker" || u.Path == "/navigation")
}
