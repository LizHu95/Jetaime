package replay

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/amap"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/qa"
)

// Exercise the real tool argument normalization and response reduction. JSON
// shape validation alone cannot catch recordings that never match a real call.
func TestToolSearchCandidateFixtures(t *testing.T) {
	root := "../../../../../evals/tool-search"
	snapshot, err := LoadSnapshot(filepath.Join(root, "messages.synthetic.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	suite, err := LoadSuite(filepath.Join(root, "cases.draft.json"), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range suite.Cases {
		t.Run(c.ID, func(t *testing.T) {
			v, err := snapshot.View(c)
			if err != nil {
				t.Fatal(err)
			}
			caller := &fixtureCaller{fixtures: c.MapFixtures}
			registry, err := tools.New(amap.New(caller, func(context.Context, tools.Scope) error { return nil })...)
			if err != nil {
				t.Fatal(err)
			}
			for _, fixture := range c.MapFixtures {
				var args map[string]any
				if err := json.Unmarshal(fixture.Arguments, &args); err != nil {
					t.Fatal(err)
				}
				if fixture.Tool == "maps_text_search" {
					delete(args, "citylimit") // The real wrapper adds this provider-only field.
				}
				if fixture.Tool == "maps_around_search" {
					radius, err := strconv.Atoi(args["radius"].(string))
					if err != nil {
						t.Fatal(err)
					}
					args["radius"] = radius // Input integer -> provider string.
				}
				raw, _ := json.Marshal(args)
				result, err := registry.Execute(context.Background(), tools.Scope{Context: v.Input, ContextEndSeq: v.Fence}, tools.Call{Name: fixture.Tool, Arguments: raw})
				if err != nil {
					t.Fatalf("%s: %v", fixture.Tool, err)
				}
				var output amap.Output
				if err := json.Unmarshal(result.Data, &output); err != nil || output.Status != "ok" {
					t.Fatalf("%s: unusable recording: %s (%v)", fixture.Tool, result.Data, err)
				}
			}
			if caller.misses != 0 {
				t.Fatal("recordings did not match normalized calls")
			}
			if c.ID == "history-meeting" || c.ID == "history-fare" {
				visible := map[string]bool{}
				for _, m := range v.Input.Messages {
					visible[m.ID] = true
				}
				for _, id := range c.RetrievedMessageIDs {
					if visible[id] {
						t.Fatal("historical evidence already in initial context")
					}
				}
				retrieved, err := v.QAContext(context.Background(), qa.ContextRequest{Scope: tools.Scope{Context: v.Input, ContextEndSeq: v.Fence}})
				if err != nil || len(retrieved.Messages) <= len(v.Input.Messages) {
					t.Fatalf("fixed retrieval did not restore old evidence: %v", err)
				}
			}
		})
	}
}
