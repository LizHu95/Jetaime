package amap

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/execution"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/respond"
)

func TestReplacementSearchExcludesDisplayedPlacesAcrossRounds(t *testing.T) {
	for _, name := range []string{"maps_text_search", "maps_around_search"} {
		t.Run(name, func(t *testing.T) {
			caller := &fakeCaller{}
			registry, err := tools.New(append(New(caller, allow), respond.New())...)
			if err != nil {
				t.Fatal(err)
			}
			s := scope()
			s.Context.RecentPlaces = []ai.RecommendedPlace{{ID: "amap:place:B001", Name: "餐厅"}}
			s.ExcludedCards = map[string]bool{}
			// Having prior recommendations alone must not block an explicit follow-up.
			normal, err := registry.Execute(context.Background(), s, tools.Call{Name: "maps_text_search", Arguments: json.RawMessage(`{"keywords":"餐厅","city":"上海"}`)})
			if err != nil || len(normal.Completion.Cards) != 1 {
				t.Fatal("ordinary follow-up was filtered", err)
			}
			s.Cards = map[string]ai.Card{normal.Completion.Cards[0].ID: normal.Completion.Cards[0]}
			args := `{"keywords":"餐厅","city":"上海","excludePrevious":true}`
			if name == "maps_around_search" {
				args = `{"keywords":"餐厅","location":"121.5,31.3","excludePrevious":true}`
			}
			replaced, err := registry.Execute(context.Background(), s, tools.Call{Name: name, Arguments: json.RawMessage(args)})
			if err != nil || len(replaced.Completion.Cards) != 0 || strings.Contains(string(replaced.Data), "B001") || !strings.Contains(string(replaced.Data), `"status":"empty"`) {
				t.Fatal("old place survived replacement", string(replaced.Data), err)
			}
			if strings.Contains(string(caller.args), "excludePrevious") {
				t.Fatal("local exclusion option leaked to provider")
			}
			again, err := registry.Execute(context.Background(), s, tools.Call{Name: "maps_text_search", Arguments: json.RawMessage(`{"keywords":"小吃","city":"上海"}`)})
			if err != nil || len(again.Completion.Cards) != 0 {
				t.Fatal("exclusion was lost between tool rounds", err)
			}
			_, err = registry.Execute(context.Background(), s, tools.Call{Name: "respond", Arguments: json.RawMessage(`{"status":"answered","answer":"换一家","sourceMessageIds":[],"cards":[{"ref":"amap:place:B001","reason":""}]}`)})
			if err == nil {
				t.Fatal("previously cached card bypassed replacement check")
			}
			// A separate invocation may still ask about the same place.
			fresh := scope()
			fresh.Context.RecentPlaces = s.Context.RecentPlaces
			fresh.ExcludedCards = map[string]bool{}
			next, err := registry.Execute(context.Background(), fresh, tools.Call{Name: "maps_text_search", Arguments: json.RawMessage(`{"keywords":"餐厅","city":"上海"}`)})
			if err != nil || len(next.Completion.Cards) != 1 {
				t.Fatal("exclusions leaked into another invocation", err)
			}
		})
	}
}

func TestPlaceFilteringKeepsNewCandidatesAndDeduplicatesIDs(t *testing.T) {
	out := excludePlaces(Output{Status: "ok", Places: []Place{{ID: "B001", Name: "旧店"}, {ID: "B002", Name: "新店"}, {ID: "B002", Name: "新店重复"}}}, map[string]bool{"amap:place:B001": true})
	if out.Status != "ok" || len(out.Places) != 1 || out.Places[0].ID != "B002" || out.Message == "" {
		t.Fatal(out)
	}
	cards := cardCandidates("maps_text_search", Search{}, out)
	if len(cards) != 1 || cards[0].ID != "amap:place:B002" {
		t.Fatal(cards)
	}
}

type refreshCaller struct{}

func (refreshCaller) Call(context.Context, string, any) (json.RawMessage, error) {
	return json.RawMessage(`{"pois":[{"id":"B001","name":"旧店"},{"id":"B002","name":"新店"},{"id":"B002","name":"新店"}]}`), nil
}

type refreshModel struct {
	t     *testing.T
	calls int
}

func (m *refreshModel) Call(_ context.Context, r llm.Request) (llm.Result, error) {
	m.calls++
	if m.calls == 1 {
		if !strings.Contains(r.Messages[1].Content, `"recentRecommendedPlaces"`) || !strings.Contains(r.Messages[1].Content, "B001") || !strings.Contains(r.Messages[0].Content, "excludePrevious=true") {
			m.t.Fatal("refresh context or instructions missing")
		}
		return llm.Result{Content: `{"name":"maps_text_search","arguments":{"keywords":"餐厅","city":"上海","excludePrevious":true}}`}, nil
	}
	if m.calls != 2 {
		m.t.Fatal("unexpected model retry")
	}
	feedback := r.Messages[len(r.Messages)-1].Content
	if strings.Contains(feedback, "B001") || !strings.Contains(feedback, "amap:place:B002") {
		m.t.Fatal("wrong refresh evidence", feedback)
	}
	return llm.Result{Content: `{"name":"respond","arguments":{"status":"answered","answer":"这次可以看看新店，来源：高德地图。","sourceMessageIds":[],"cards":[{"ref":"amap:place:B002","reason":"换一家看看"}]}}`}, nil
}
func TestMainRefreshReturnsOnlyNewPlaceCards(t *testing.T) {
	registry, err := tools.New(append(New(refreshCaller{}, allow), respond.New())...)
	if err != nil {
		t.Fatal(err)
	}
	model := &refreshModel{t: t}
	service, err := execution.New(model, "test", 32768, registry)
	if err != nil {
		t.Fatal(err)
	}
	input := scope().Context
	input.Messages[0].Text = "换一批"
	input.RecentPlaces = []ai.RecommendedPlace{{ID: "amap:place:B001", Name: "旧店"}}
	run, err := service.Execute(context.Background(), ai.Invocation{Mode: "main", Context: input, Question: "换一批"})
	if err != nil || len(run.Cards) != 1 || run.Cards[0].ID != "amap:place:B002" || model.calls != 2 {
		t.Fatalf("replacement failed: %+v %v", run, err)
	}
}
