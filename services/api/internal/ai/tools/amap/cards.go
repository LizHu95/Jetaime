package amap

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"net/url"
	"strconv"
	"strings"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
)

func cardCandidates(name string, args any, out Output, privateOrigin ...bool) []ai.Card {
	if out.Status != "ok" {
		return nil
	}
	cards := []ai.Card{}
	for _, p := range out.Places {
		if !placeID(p.ID) || p.Name == "" {
			continue
		}
		q := url.Values{"poiid": {p.ID}, "name": {p.Name}, "src": {"jetaime"}, "callnative": {"0"}}
		card := ai.Card{ID: "amap:place:" + p.ID, Type: "place", Title: p.Name, Source: "amap", Place: &ai.PlaceCard{
			Name: p.Name, Address: p.Address, City: p.City, CostPerPerson: metric(p.Cost), Rating: metric(p.Rating), OpeningHours: p.OpeningHours, MapURL: "https://uri.amap.com/marker?" + q.Encode(),
		}}
		if card.Place.Rating != nil && *card.Place.Rating > 5 {
			card.Place.Rating = nil
		}
		if ai.ValidateCards([]ai.Card{card}) == nil {
			cards = append(cards, card)
		}
	}
	mode := strings.TrimPrefix(name, "maps_direction_")
	if mode == "transit_integrated" {
		mode = "transit"
	}
	mapMode := map[string]string{"walking": "walk", "driving": "car", "bicycling": "ride", "transit": "bus"}[mode]
	if len(out.Routes) == 0 || mapMode == "" {
		return cards
	}
	raw, _ := json.Marshal(args)
	var endpoints struct {
		Origin      string `json:"origin"`
		Destination string `json:"destination"`
	}
	if json.Unmarshal(raw, &endpoints) != nil || !coordinate(endpoints.Origin) || !coordinate(endpoints.Destination) {
		return cards
	}
	q := url.Values{"from": {endpoints.Origin}, "to": {endpoints.Destination}, "mode": {mapMode}, "src": {"jetaime"}, "callnative": {"0"}}
	mapURL := "https://uri.amap.com/navigation?" + q.Encode()
	// Shared cards never contain the private client origin. Show the destination instead.
	if len(privateOrigin) > 0 && privateOrigin[0] {
		mapURL = "https://uri.amap.com/marker?" + url.Values{"position": {endpoints.Destination}, "name": {"目的地"}, "src": {"jetaime"}, "callnative": {"0"}}.Encode()
	}
	for index, r := range out.Routes {
		hash := sha256.Sum256(append([]byte(name+":"+strconv.Itoa(index)+":"), raw...))
		card := ai.Card{ID: "amap:route:" + hex.EncodeToString(hash[:12]), Type: "route", Source: "amap", Title: map[string]string{"walking": "步行", "driving": "驾车", "bicycling": "骑行", "transit": "公交地铁"}[mode] + "方案 " + strconv.Itoa(index+1), Route: &ai.RouteCard{
			Mode: mode, DistanceMeters: metric(r.Distance), DurationSeconds: metric(r.Duration), WalkingDistanceMeters: metric(r.WalkingDistance), Instructions: r.Instructions, MapURL: mapURL,
		}}
		if ai.ValidateCards([]ai.Card{card}) == nil {
			cards = append(cards, card)
		}
	}
	return cards
}
func metric(raw string) *float64 {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	n, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n > 1e10 {
		return nil
	}
	return &n
}
