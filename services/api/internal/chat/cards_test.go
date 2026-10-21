package chat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/auth"
	"github.com/LizHu95/Jetaime/services/api/internal/httpapi"
	"github.com/google/uuid"
)

func TestCardsPersistAndRespectMessagePermissions(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	if _, err := f.service.Send(ctx, f.group, f.users[0].ID, SendInput{Content: "@AI 中午吃什么", ClientID: uuid.NewString()}); err != nil {
		t.Fatal(err)
	}
	run, err := f.store.Claim(ctx, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	result, text, err := ai.ResultFromRun(ai.Run{Kind: "main", Answer: &ai.Answer{Status: "answered", Answer: "看看这家。", SourceMessageIDs: []string{}}, Cards: []ai.Card{{ID: "amap:place:B001", Type: "place", Title: "大学路粥铺", Source: "amap", Reason: "可选去处", Place: &ai.PlaceCard{Name: "大学路粥铺", Address: "大学路88号", MapURL: "https://uri.amap.com/marker?poiid=B001"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.Complete(ctx, run, text, "", &result); err != nil {
		t.Fatal(err)
	}
	router := httpapi.NewRouter(httpapi.Handlers{Chat: NewHandler(f.service, auth.NewHandler(f.auth), nil)})
	for _, member := range []int{0, 1, 2} {
		r := httptest.NewRequest(http.MethodGet, "/api/chat/groups/"+f.group+"/messages?afterSeq=0", nil)
		r.Header.Set("Authorization", "Bearer "+f.tokens[member])
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if member == 2 {
			if w.Code == 200 || strings.Contains(w.Body.String(), "大学路粥铺") {
				t.Fatal("nonmember read cards")
			}
			continue
		}
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var page Page
		if json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Messages) != 2 || page.Messages[1].AgentResult == nil || len(page.Messages[1].AgentResult.Cards) != 1 {
			t.Fatal(w.Body.String())
		}
		if !strings.Contains(w.Body.String(), `"costPerPerson":null`) {
			t.Fatal("unknown cost lost", w.Body.String())
		}
	}
}

func TestRecentPlaceHistoryContainsOnlyDisplayedCardsAndIsBounded(t *testing.T) {
	input := ai.Context{}
	card := ai.Card{ID: "amap:place:B001", Type: "place", Source: "amap", Title: "粥铺", Place: &ai.PlaceCard{Name: "粥铺", MapURL: "https://uri.amap.com/marker?poiid=B001"}}
	addRecentPlaces(&input, nil)
	addRecentPlaces(&input, &ai.Result{Cards: []ai.Card{card, card, {ID: "invalid", Type: "place"}}})
	if len(input.RecentPlaces) != 1 || input.RecentPlaces[0].ID != card.ID || input.RecentPlaces[0].Name != "粥铺" {
		t.Fatal(input.RecentPlaces)
	}
	for i := 0; i < 30; i++ {
		next := card
		next.ID = "amap:place:" + uuid.NewString()
		addRecentPlaces(&input, &ai.Result{Cards: []ai.Card{next}})
	}
	if len(input.RecentPlaces) != 20 {
		t.Fatal("unbounded recommendation history", len(input.RecentPlaces))
	}
}

func TestPostgresRefreshContextCarriesDisplayedPlacesAtOriginalFence(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	complete := func(id string) Run {
		t.Helper()
		if _, err := f.service.Send(ctx, f.group, f.users[0].ID, SendInput{Content: "@AI 推荐", ClientID: uuid.NewString()}); err != nil {
			t.Fatal(err)
		}
		job, err := f.store.Claim(ctx, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		result := ai.Result{Kind: "main", Cards: []ai.Card{{ID: id, Type: "place", Source: "amap", Title: "店名", Place: &ai.PlaceCard{Name: "店名", MapURL: "https://uri.amap.com/marker?poiid=B001"}}}}
		if err := f.store.Complete(ctx, job, "推荐这家", "", &result); err != nil {
			t.Fatal(err)
		}
		return job
	}
	complete("amap:place:B001")
	if _, err := f.service.Send(ctx, f.group, f.users[0].ID, SendInput{Content: "@AI 换一批", ClientID: uuid.NewString()}); err != nil {
		t.Fatal(err)
	}
	job, err := f.store.Claim(ctx, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// Later recommendations must never change a replay of this original request.
	complete("amap:place:B002")
	input, _, err := f.store.AgentContext(ctx, job, 32768)
	if err != nil || len(input.RecentPlaces) != 1 || input.RecentPlaces[0].ID != "amap:place:B001" {
		t.Fatalf("display history missing or crossed trigger fence: %+v %v", input.RecentPlaces, err)
	}
}
