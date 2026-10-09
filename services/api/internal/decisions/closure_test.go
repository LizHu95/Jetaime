package decisions

import (
	"testing"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/notes"
)

func TestChangedIntentStartsNewSession(t *testing.T) {
	now := time.Now()
	s := Session{ID: "s", SpaceID: "space", RequesterID: "a", Task: TaskSelect, Query: "Dinner", ExpiresAt: now.Add(time.Hour), Conditions: Conditions{HardConstraints: []string{"quiet", "no peanuts"}, NoteTypes: []notes.Type{notes.TypeRestaurant, notes.TypeRecipe}}}
	r := Request{SessionID: s.ID, SpaceID: s.SpaceID, RequesterID: s.RequesterID, Task: s.Task, Query: " Dinner ", Conditions: Conditions{HardConstraints: []string{" no peanuts ", "quiet", "quiet"}, NoteTypes: []notes.Type{notes.TypeRecipe, notes.TypeRestaurant}}}
	if err := s.ValidateFor(r, now); err != nil {
		t.Fatal("set order and whitespace changed intent", err)
	}
	r.Query = "Movie"
	if s.ValidateFor(r, now) == nil {
		t.Fatal("new query reused exclusions")
	}
	r.Query = s.Query
	zero := int64(0)
	r.Conditions.BudgetMaxCents = &zero
	if s.ValidateFor(r, now) == nil {
		t.Fatal("explicit zero treated as unrestricted")
	}
}

func TestSessionFeedbackAcrossBatches(t *testing.T) {
	now := time.Now()
	_, r := selectFixture()
	s := Session{ID: "s", SpaceID: "space", RequesterID: "a", Task: TaskSelect, Query: "Dinner", LatestDecisionID: "new", ExpiresAt: now.Add(time.Hour)}
	old := Decision{ID: "old", SessionID: s.ID, SpaceID: s.SpaceID, RequesterID: s.RequesterID, Task: s.Task, Query: s.Query, ParticipantIDs: []string{"a", "b"}, Result: r}
	f := Feedback{ID: "reject", DecisionID: old.ID, UserID: "b", Action: FeedbackReject, OptionID: "o", CreatedAt: now}
	if err := s.ApplyDecisionFeedback(&old, f, "b", now); err != nil || len(s.ExcludedNoteIDs) != 1 {
		t.Fatal("partner old-batch rejection not applied", err)
	}
	f.ID, f.Action = "adopt", FeedbackAdopt
	if err := s.ApplyDecisionFeedback(&old, f, "b", now); err != nil || s.AdoptedOption.DecisionID != old.ID || len(s.ExcludedNoteIDs) != 0 {
		t.Fatal("re-adoption not projected", err)
	}
	newer := old
	newer.ID, newer.Feedback = "new", nil
	newer.Result.Options = append([]Option(nil), old.Result.Options...)
	newer.Result.Options[0].Selection = &Selection{NoteID: "n2"}
	f.ID, f.DecisionID, f.UserID = "new-adopt", newer.ID, "a"
	if err := s.ApplyDecisionFeedback(&newer, f, "a", now); err != nil || s.AdoptedOption.DecisionID != newer.ID {
		t.Fatal("adoption across batches not replaced", err)
	}
	f.ID, f.DecisionID, f.UserID, f.Action = "retry-old", old.ID, "b", FeedbackReject
	if err := s.ApplyDecisionFeedback(&old, f, "b", now); err != nil || s.AdoptedOption.DecisionID != newer.ID {
		t.Fatal("old rejection cleared another option ref", err)
	}
	f.ID, f.Action, f.OptionID = "batch", FeedbackChangeBatch, ""
	before := len(old.Feedback)
	if s.ApplyDecisionFeedback(&old, f, "b", now) == nil || len(old.Feedback) != before {
		t.Fatal("stale batch command accepted")
	}
	f.DecisionID = newer.ID
	if err := s.ApplyDecisionFeedback(&newer, f, "b", now); err != nil {
		t.Fatal(err)
	}
	s.LatestDecisionID = "next"
	if err := s.ApplyDecisionFeedback(&newer, f, "b", now); err != nil {
		t.Fatal("successful batch retry lost idempotency", err)
	}
	f.ID, f.DecisionID, f.Action, f.OptionID = "expired", old.ID, FeedbackAdopt, "o"
	if err := s.ApplyDecisionFeedback(&old, f, "b", s.ExpiresAt); err != nil || s.AdoptedOption.DecisionID != newer.ID {
		t.Fatal("expired feedback changed session projection", err)
	}
	f.ID = "foreign"
	old.SessionID = "another"
	if s.ApplyDecisionFeedback(&old, f, "b", now) == nil {
		t.Fatal("cross-session feedback accepted")
	}
}

func TestRejectSameNoteAcrossBatchesClearsSessionAdoption(t *testing.T) {
	now := time.Now()
	_, r := selectFixture()
	s := Session{ID: "s", SpaceID: "space", RequesterID: "a", Task: TaskSelect, Query: "Dinner", ExpiresAt: now.Add(time.Hour), AdoptedOption: &OptionRef{DecisionID: "new", OptionID: "new-option", NoteID: "n"}}
	d := Decision{ID: "old", SessionID: s.ID, SpaceID: s.SpaceID, RequesterID: s.RequesterID, Task: s.Task, Query: s.Query, ParticipantIDs: []string{"a", "b"}, Result: r}
	f := Feedback{ID: "f", DecisionID: d.ID, UserID: "b", Action: FeedbackReject, OptionID: "o", CreatedAt: now}
	if err := s.ApplyDecisionFeedback(&d, f, "b", now); err != nil || s.AdoptedOption != nil || len(s.ExcludedNoteIDs) != 1 {
		t.Fatal("same note remained both adopted and excluded", err)
	}
}

func TestEvaluationDistinguishesUnknownFromConflict(t *testing.T) {
	cases := []struct {
		name       string
		evaluation SelectEvaluation
		want       Outcome
	}{
		{"empty", SelectEvaluation{}, OutcomeNoCandidates},
		{"contradictory input", SelectEvaluation{ConditionsConflict: true}, OutcomeConstraintConflict},
		{"all violated", SelectEvaluation{Candidates: []CandidateAssessment{{"n", ConstraintViolated}}}, OutcomeConstraintConflict},
		{"unknown remains", SelectEvaluation{Candidates: []CandidateAssessment{{"n", ConstraintViolated}, {"n2", ConstraintUnknown}}}, OutcomeInsufficientInfo},
		{"some verified", SelectEvaluation{Candidates: []CandidateAssessment{{"n", ConstraintSatisfied}, {"n2", ConstraintUnknown}}}, OutcomeRecommended},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.evaluation.Outcome()
			if err != nil || got != tc.want {
				t.Fatal(got, err)
			}
		})
	}
	ctx, r := selectFixture()
	e := SelectEvaluation{Candidates: []CandidateAssessment{{"n", ConstraintUnknown}, {"n2", ConstraintSatisfied}}}
	if e.ValidateResult(ctx, r) == nil {
		t.Fatal("unknown candidate recommended while another is safe")
	}
	e.Candidates[0].Status = ConstraintSatisfied
	if err := e.ValidateResult(ctx, r); err != nil {
		t.Fatal(err)
	}
	r.Outcome, r.Options = OutcomeNoCandidates, nil
	if e.ValidateResult(ctx, r) == nil {
		t.Fatal("false no_candidates accepted")
	}
	r.Outcome = OutcomeInsufficientInfo
	ctx.Participants = nil
	if r.ValidateSelect(ctx) == nil {
		t.Fatal("non-recommended result bypassed context validity")
	}
}
