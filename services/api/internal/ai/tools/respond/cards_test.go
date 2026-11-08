package respond

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
)

func cardScope() tools.Scope {
	return tools.Scope{Context: ai.Context{ActorID: "u", GroupID: "g", Users: []ai.User{{ID: "u", Nickname: "用户"}}}, Cards: map[string]ai.Card{
		"amap:place:B001": {ID: "amap:place:B001", Type: "place", Source: "amap", Title: "真实店名", Place: &ai.PlaceCard{Name: "真实店名", Address: "真实地址", MapURL: "https://uri.amap.com/marker?poiid=B001"}},
	}}
}
func TestCardSelectionUsesOnlyServerFacts(t *testing.T) {
	scope := cardScope()
	result, err := New().Invoke(context.Background(), scope, json.RawMessage(`{"status":"answered","answer":"可以看看这家。","sourceMessageIds":[],"cards":[{"ref":"amap:place:B001","reason":"符合这次的需求"}]}`))
	if err != nil || len(result.Completion.Cards) != 1 {
		t.Fatal(result, err)
	}
	card := result.Completion.Cards[0]
	if card.Place.Name != "真实店名" || card.Place.CostPerPerson != nil || card.Reason != "符合这次的需求" || scope.Cards[card.ID].Reason != "" {
		t.Fatal("facts changed or selection mutated catalog", card)
	}
	persisted, _, err := ai.ResultFromRun(ai.Run{Kind: "main", Answer: result.Completion.Answer, Cards: result.Completion.Cards})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(persisted)
	if !strings.Contains(string(data), `"costPerPerson":null`) || !strings.Contains(string(data), `"cards"`) {
		t.Fatal(string(data))
	}
}
func TestForgedOrCrossInvocationCardsAreRejected(t *testing.T) {
	for _, selection := range []string{
		`[{"ref":"missing","reason":""}]`,
		`[{"ref":"amap:place:B001","reason":"","place":{"name":"假的","costPerPerson":1}}]`,
		`[{"ref":"amap:place:B001","reason":""},{"ref":"amap:place:B001","reason":""}]`,
	} {
		raw := json.RawMessage(`{"status":"answered","answer":"推荐","sourceMessageIds":[],"cards":` + selection + `}`)
		if _, err := New().Invoke(context.Background(), cardScope(), raw); err == nil {
			t.Fatal("forged cards accepted", selection)
		}
	}
	scope := cardScope()
	scope.Cards = nil
	if _, err := New().Invoke(context.Background(), scope, json.RawMessage(`{"status":"answered","answer":"推荐","sourceMessageIds":[],"cards":[{"ref":"amap:place:B001","reason":""}]}`)); err == nil {
		t.Fatal("previous invocation card accepted")
	}
}
