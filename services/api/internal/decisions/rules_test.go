package decisions

import (
	"testing"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/notes"
)

func selectFixture() (Context, Result) {
	ctx := Context{Task: TaskSelect, Query: "Dinner", Participants: []Participant{{UserID: "a"}, {UserID: "b"}}, Candidates: []notes.Note{{ID: "n", CreatorID: "a", Title: "Restaurant", Type: notes.TypeRestaurant}}}
	r := Result{Outcome: OutcomeRecommended, Explanation: "One match", Options: []Option{{OptionID: "o", Title: "Dinner", Selection: &Selection{NoteID: "n"}, Reason: "Suitable", ParticipantMatches: []ParticipantMatch{{UserID: "a", Explanation: "Quiet"}, {UserID: "b", Explanation: "Light food"}}}}}
	return ctx, r
}

func TestSelectResultBoundaries(t *testing.T) {
	ctx, result := selectFixture()
	if err := result.ValidateSelect(ctx); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		mutate func(*Context, *Result)
	}{
		{"unsupported planning", func(c *Context, r *Result) { c.Task = TaskDayPlan }},
		{"both payloads", func(c *Context, r *Result) { r.Options[0].Itinerary = &Itinerary{} }},
		{"missing payload", func(c *Context, r *Result) { r.Options[0].Selection = nil }},
		{"foreign note", func(c *Context, r *Result) { r.Options[0].Selection.NoteID = "foreign" }},
		{"duplicate option", func(c *Context, r *Result) { r.Options = append(r.Options, r.Options[0]) }},
		{"too many alternatives", func(c *Context, r *Result) {
			r.Options = []Option{r.Options[0], r.Options[0], r.Options[0], r.Options[0]}
		}},
		{"same note in different options", func(c *Context, r *Result) {
			second := r.Options[0]
			second.OptionID = "other"
			r.Options = append(r.Options, second)
		}},
		{"missing partner explanation", func(c *Context, r *Result) { r.Options[0].ParticipantMatches = r.Options[0].ParticipantMatches[:1] }},
		{"invented participant", func(c *Context, r *Result) { r.Options[0].ParticipantMatches[0].UserID = "outsider" }},
		{"type filter", func(c *Context, r *Result) { c.Conditions.NoteTypes = []notes.Type{notes.TypeMovie} }},
		{"false no candidates", func(c *Context, r *Result) { r.Outcome = OutcomeNoCandidates }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, r := selectFixture()
			tc.mutate(&c, &r)
			if r.ValidateSelect(c) == nil {
				t.Fatal("invalid output accepted")
			}
		})
	}
	if err := (Result{Outcome: OutcomeNoCandidates, Explanation: "No matches"}).ValidateSelect(Context{Task: TaskSelect, Participants: []Participant{{UserID: "a"}}}); err != nil {
		t.Fatal(err)
	}
}

func TestFeedbackRetargetRetryAndHistory(t *testing.T) {
	_, r := selectFixture()
	second := r.Options[0]
	second.OptionID = "other"
	second.Selection = &Selection{NoteID: "n2"}
	r.Options = append(r.Options, second)
	d := Decision{ID: "d", Task: TaskSelect, ParticipantIDs: []string{"a", "b"}, Result: r}
	now := time.Now()
	f := Feedback{ID: "f1", DecisionID: "d", UserID: "a", Action: FeedbackAdopt, OptionID: "o", CreatedAt: now}
	if err := d.ApplyFeedback(f, "a"); err != nil {
		t.Fatal(err)
	}
	f.CreatedAt = now.Add(time.Minute)
	if err := d.ApplyFeedback(f, "a"); err != nil || len(d.Feedback) != 1 {
		t.Fatal("retry duplicated feedback", err)
	}
	f.OptionID = "other"
	if d.ApplyFeedback(f, "a") == nil {
		t.Fatal("same event id silently changed payload")
	}
	f.ID, f.UserID = "f2", "b"
	if err := d.ApplyFeedback(f, "b"); err != nil || d.AdoptedOptionID() != "other" || len(d.Feedback) != 2 {
		t.Fatal("adoption replacement lost event history", err)
	}
	f.ID, f.Action = "f3", FeedbackReject
	if err := d.ApplyFeedback(f, "b"); err != nil || d.AdoptedOptionID() != "" {
		t.Fatal("rejecting current selection did not clear it", err)
	}
	f.ID, f.Action = "f4", FeedbackChangeBatch
	if d.ApplyFeedback(f, "b") == nil {
		t.Fatal("batch feedback accepted option target")
	}
	f.OptionID = ""
	if err := d.ApplyFeedback(f, "b"); err != nil {
		t.Fatal(err)
	}
	f.ID, f.UserID = "f5", "outsider"
	if d.ApplyFeedback(f, "outsider") == nil {
		t.Fatal("outsider feedback accepted")
	}
}

func TestSessionScopeAndRequestLimits(t *testing.T) {
	now := time.Now()
	r := Request{SpaceID: "s", RequesterID: "a", Task: TaskSelect, Query: "Dinner", SessionID: "session"}
	s := Session{ID: "session", SpaceID: "s", RequesterID: "a", Task: TaskSelect, Query: "Dinner", ExpiresAt: now.Add(time.Minute)}
	if err := s.ValidateFor(r, now); err != nil {
		t.Fatal(err)
	}
	r.SpaceID = "other"
	if s.ValidateFor(r, now) == nil {
		t.Fatal("session crossed spaces")
	}
	r.SpaceID = "s"
	if s.ValidateFor(r, s.ExpiresAt) == nil {
		t.Fatal("expired session accepted")
	}
	r.Conditions.Planning = &PlanningConditions{}
	if r.ValidateSelect() == nil {
		t.Fatal("select accepted planning input")
	}
	r.Conditions.Planning = nil
	zero := int64(0)
	r.Conditions.BudgetMaxCents = &zero
	if err := r.ValidateSelect(); err != nil {
		t.Fatal("explicit zero rejected", err)
	}
	negative := int64(-1)
	r.Conditions.BudgetMaxCents = &negative
	if r.ValidateSelect() == nil {
		t.Fatal("negative budget accepted")
	}
}
