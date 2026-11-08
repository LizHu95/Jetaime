package aicontext_test

import (
	"context"
	"strings"
	"testing"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	aicontext "github.com/LizHu95/Jetaime/services/api/internal/ai/context"
)

func TestExecutionPinsEvidenceAndFillsSixRecentOriginals(t *testing.T) {
	snapshot := initialContextFixture(t).Snapshot
	snapshot.Request = &ai.RequestContext{MessageID: snapshot.Messages[29].ID, SenderID: snapshot.ActorID, TriggerMode: "proactive", RelatedMessageIDs: []string{snapshot.Messages[0].ID, snapshot.Messages[3].ID}}
	snapshot.HistoryScope = &ai.HistoryScope{Policy: "proactive-recent-30-v1"}
	selected, err := aicontext.ExecutionContext(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected.Messages) != 9 || selected.Messages[0] != snapshot.Messages[0] || selected.Messages[1] != snapshot.Messages[3] || !selected.HistoryScope.EarlierMessagesOmitted {
		t.Fatal("required evidence or six-message limit lost")
	}
	for i, message := range selected.Messages[2:] {
		if message != snapshot.Messages[23+i] {
			t.Fatal("latest originals changed or reordered")
		}
	}
	if selected.Request != snapshot.Request || len(snapshot.Messages) != 30 || snapshot.HistoryScope.Policy != "proactive-recent-30-v1" || snapshot.HistoryScope.EarlierMessagesOmitted {
		t.Fatal("recognition snapshot or private request metadata mutated")
	}
}

func TestExecutionBoundsBytesAndRejectsMissingRequiredEvidence(t *testing.T) {
	snapshot := initialContextFixture(t).Snapshot
	snapshot.Request = &ai.RequestContext{MessageID: snapshot.Messages[29].ID, SenderID: snapshot.ActorID, RelatedMessageIDs: []string{snapshot.Messages[0].ID}}
	snapshot.Messages[0].Text = strings.Repeat("required", 1000)
	snapshot.Messages[28].Text = strings.Repeat("background", 1000)
	selected, err := aicontext.ExecutionContext(snapshot)
	if err != nil || len(selected.Messages) != 2 || selected.Messages[0] != snapshot.Messages[0] {
		t.Fatal("optional byte limit removed required text", err)
	}
	snapshot.Request.RelatedMessageIDs = append(snapshot.Request.RelatedMessageIDs, "not-loaded")
	if _, err = aicontext.ExecutionContext(snapshot); err == nil {
		t.Fatal("missing required evidence silently ignored")
	}
}

func TestRecentRecommendedPlacesSurviveContextSelection(t *testing.T) {
	request := initialContextFixture(t)
	request.Snapshot.RecentPlaces = []ai.RecommendedPlace{{ID: "amap:place:B001", Name: "已推荐的店"}}
	input, err := aicontext.InjectDefaultContext(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	input, err = aicontext.ExecutionContext(input)
	if err != nil || len(input.RecentPlaces) != 1 || input.RecentPlaces[0].ID != "amap:place:B001" {
		t.Fatal("display history lost", err)
	}
}
