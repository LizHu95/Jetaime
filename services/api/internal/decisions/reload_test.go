package decisions

import (
	"testing"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/notes"
)

func TestWithDatasetPreservesHistoryAndExpiresSessions(t *testing.T) {
	store, err := NewMemoryStore(Dataset{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	oldNote := notes.Note{ID: "note", CreatorID: "user", Title: "旧标题", Type: notes.TypeRestaurant}
	decision := Decision{ID: "decision", SessionID: "session", CandidateNotes: []notes.Note{oldNote}}
	session := Session{ID: "session", LatestDecisionID: decision.ID, ExpiresAt: now.Add(time.Hour)}
	if err := store.commitGenerated(decision, session, 0, ""); err != nil {
		t.Fatal(err)
	}
	newNote := oldNote
	newNote.Title = "新标题"
	data := Dataset{Notes: []notes.Note{newNote}}
	updated, err := store.WithDataset(data, now)
	if err != nil {
		t.Fatal(err)
	}
	data.Notes[0].Title = "外部修改"
	snapshot, err := updated.loadDecision(decision.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Decision.CandidateNotes[0].Title != "旧标题" || snapshot.Data.Notes[0].Title != "新标题" || snapshot.Session.ExpiresAt.After(now) {
		t.Fatal("history, owned data or session expiration incorrect")
	}
	original, err := store.loadSession(session.ID)
	if err != nil || !original.Session.ExpiresAt.After(now) {
		t.Fatal("original store changed", err)
	}
	if _, err := store.WithDataset(Dataset{Notes: []notes.Note{{ID: "invalid"}}}, now); err == nil {
		t.Fatal("invalid dataset accepted")
	}
}
