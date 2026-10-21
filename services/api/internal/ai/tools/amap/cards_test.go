package amap

import (
	"net/url"
	"testing"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
)

func TestCardFactsAndMapLinks(t *testing.T) {
	cards := cardCandidates("maps_search_detail", Detail{ID: "B001"}, Output{Status: "ok", Places: []Place{{ID: "B001", Name: "粥 & 汤", Cost: "", Rating: "4.5", OpeningHours: "10:00-20:00"}}})
	if len(cards) != 1 || ai.ValidateCards(cards) != nil || cards[0].Place.CostPerPerson != nil || cards[0].Place.Rating == nil {
		t.Fatal(cards)
	}
	u, err := url.Parse(cards[0].Place.MapURL)
	if err != nil || u.Host != "uri.amap.com" || u.Query().Get("poiid") != "B001" || u.Query().Get("name") != "粥 & 汤" || u.Query().Has("key") {
		t.Fatal("bad public map URL")
	}
	cards = cardCandidates("maps_direction_transit_integrated", Transit{Origin: "121.5,31.3", Destination: "121.51,31.31", City: "上海", CityD: "上海"}, Output{Status: "ok", Routes: []Route{{Distance: "1000", Duration: "600", Instructions: []string{"10号线"}}}})
	if len(cards) != 1 || ai.ValidateCards(cards) != nil || *cards[0].Route.DurationSeconds != 600 {
		t.Fatal(cards)
	}
	u, _ = url.Parse(cards[0].Route.MapURL)
	if u.Query().Get("mode") != "bus" || u.Query().Get("from") != "121.5,31.3" {
		t.Fatal(u)
	}
	if len(cardCandidates("maps_text_search", Search{}, Output{Status: "unavailable", Places: []Place{{ID: "B001", Name: "店"}}})) != 0 {
		t.Fatal("failed query created card")
	}
}
