package mainagent

import "github.com/LizHu95/Jetaime/services/api/internal/ai"

type cardReference struct {
	Ref   string `json:"ref"`
	Type  string `json:"type"`
	Title string `json:"title"`
}

// Catalogs exist only for this invocation, never across users or conversations.
func collectCards(catalog map[string]ai.Card, cards []ai.Card) []cardReference {
	refs := []cardReference{}
	for _, card := range cards {
		if ai.ValidateCards([]ai.Card{card}) != nil {
			continue
		}
		if old, ok := catalog[card.ID]; ok && card.Place != nil && old.Place != nil {
			p := *card.Place
			if p.Address == "" {
				p.Address = old.Place.Address
			}
			if p.City == "" {
				p.City = old.Place.City
			}
			if p.CostPerPerson == nil {
				p.CostPerPerson = old.Place.CostPerPerson
			}
			if p.Rating == nil {
				p.Rating = old.Place.Rating
			}
			if p.OpeningHours == "" {
				p.OpeningHours = old.Place.OpeningHours
			}
			card.Place = &p
		}
		catalog[card.ID] = card
		refs = append(refs, cardReference{card.ID, card.Type, card.Title})
	}
	return refs
}
