package demo

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/decisions"
	"github.com/LizHu95/Jetaime/services/api/internal/memory"
	"github.com/LizHu95/Jetaime/services/api/internal/notes"
)

type providerFunc func(context.Context, decisions.Context) (decisions.Result, error)

func (f providerFunc) Generate(ctx context.Context, input decisions.Context) (decisions.Result, error) {
	return f(ctx, input)
}

type checkerFunc func(context.Context, decisions.Context) (decisions.SelectEvaluation, error)

func (f checkerFunc) Evaluate(ctx context.Context, input decisions.Context) (decisions.SelectEvaluation, error) {
	return f(ctx, input)
}

func serviceFor(t *testing.T, fixture Fixture, provider decisions.Provider, clock func() time.Time) *decisions.DecisionService {
	t.Helper()
	store, err := decisions.NewMemoryStore(fixture.Dataset)
	if err != nil {
		t.Fatal(err)
	}
	service, err := decisions.NewDecisionService(store, Checker{Facts: fixture.Facts}, provider, decisions.ServiceConfig{Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	return service
}
func requestFor(t *testing.T, f Fixture, name string) decisions.Request {
	t.Helper()
	scenario, err := f.Scenario(name)
	if err != nil {
		t.Fatal(err)
	}
	return scenario.Request
}
func feedback(t *testing.T, s *decisions.DecisionService, d decisions.Decision, id string, action decisions.FeedbackAction, index int, actor string) decisions.Session {
	t.Helper()
	state, err := s.Feedback(context.Background(), decisions.Feedback{ID: id, DecisionID: d.ID, Action: action, OptionID: d.Result.Options[index].OptionID}, actor)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestFixtureCountsAndOutcomes(t *testing.T) {
	f := NewFixture()
	counts := map[notes.Type]int{}
	memories := map[string]int{}
	for _, n := range f.Dataset.Notes {
		counts[n.Type]++
	}
	for _, m := range f.Dataset.Memories {
		memories[m.UserID]++
	}
	if len(f.Dataset.Notes) != 50 || counts[notes.TypeRestaurant] != 20 || counts[notes.TypeRecipe] != 15 || counts[notes.TypeActivity] != 10 || counts[notes.TypeMovie] != 5 || memories[UserA] != 12 || memories[UserB] != 12 {
		t.Fatal(counts, memories)
	}
	for _, scenario := range f.Scenarios {
		t.Run(scenario.Name, func(t *testing.T) {
			calls := 0
			p := providerFunc(func(ctx context.Context, input decisions.Context) (decisions.Result, error) {
				calls++
				return (FixedProvider{}).Generate(ctx, input)
			})
			service := serviceFor(t, f, p, nil)
			d, err := service.Generate(context.Background(), scenario.Request, scenario.Request.RequesterID)
			if err != nil || d.Result.Outcome != scenario.Expected {
				t.Fatal(d.Result.Outcome, err)
			}
			if scenario.Expected != decisions.OutcomeRecommended && (calls != 0 || len(d.Result.Options) != 0) {
				t.Fatal("non-recommended outcome called provider")
			}
		})
	}
}

func TestContextPrivacyAndAuthorization(t *testing.T) {
	f := NewFixture()
	var captured decisions.Context
	p := providerFunc(func(ctx context.Context, input decisions.Context) (decisions.Result, error) {
		captured = input
		return (FixedProvider{}).Generate(ctx, input)
	})
	s := serviceFor(t, f, p, nil)
	d, err := s.Generate(context.Background(), requestFor(t, f, "normal"), UserA)
	if err != nil {
		t.Fatal(err)
	}
	if len(captured.Participants) != 2 {
		t.Fatal("partner missing")
	}
	hard := 0
	for _, participant := range captured.Participants {
		hard += len(participant.HardConstraints)
		for _, m := range append(slices.Clone(participant.HardConstraints), participant.RelevantMemories...) {
			if strings.Contains(m.Content, "私有") || m.Source == memory.SourceInferred && !m.Confirmed {
				t.Fatal("private or unconfirmed memory leaked", m.ID)
			}
		}
	}
	if hard != 1 {
		t.Fatal("hard constraint missing")
	}
	for _, n := range d.CandidateNotes {
		if n.ID == "restaurant-20" {
			t.Fatal("private note leaked")
		}
	}
	for _, n := range captured.Candidates {
		fact := f.Facts[n.ID]
		if !fact.IngredientsKnown || slices.Contains(fact.Ingredients, "花生") {
			t.Fatal("unsafe/unknown note reached provider")
		}
	}
	if _, err := s.Generate(context.Background(), requestFor(t, f, "personal"), UserA); err != nil {
		t.Fatal(err)
	}
	if len(captured.Participants) != 1 || captured.Participants[0].UserID != UserA {
		t.Fatal("personal participants incorrect")
	}
	private := false
	for _, m := range captured.Participants[0].RelevantMemories {
		private = private || strings.Contains(m.Content, "私有")
	}
	if !private {
		t.Fatal("own private memory missing")
	}
	for _, n := range captured.Candidates {
		if n.CreatorID != UserA {
			t.Fatal("partner private note leaked")
		}
	}
	request := requestFor(t, f, "personal")
	request.RequesterID = UserB
	if _, err := s.Generate(context.Background(), request, UserB); !errors.Is(err, decisions.ErrForbidden) {
		t.Fatal("cross-space accepted", err)
	}
	if _, err := s.Generate(context.Background(), requestFor(t, f, "normal"), UserB); !errors.Is(err, decisions.ErrForbidden) {
		t.Fatal("forged requester accepted", err)
	}
}

func TestFeedbackBatchAndFreshSession(t *testing.T) {
	f := NewFixture()
	s := serviceFor(t, f, FixedProvider{}, nil)
	ctx := context.Background()
	request := requestFor(t, f, "normal")
	first, err := s.Generate(ctx, request, UserA)
	if err != nil {
		t.Fatal(err)
	}
	feedback(t, s, first, "adopt", decisions.FeedbackAdopt, 0, UserA)
	state := feedback(t, s, first, "reject", decisions.FeedbackReject, 1, UserB)
	rejected := first.Result.Options[1].Selection.NoteID
	if !slices.Contains(state.ExcludedNoteIDs, rejected) {
		t.Fatal("no exclusion")
	}
	feedback(t, s, first, "reject", decisions.FeedbackReject, 1, UserB)
	history, err := s.GetDecision(first.ID, UserA)
	if err != nil || len(history.Feedback) != 2 {
		t.Fatal("duplicate feedback", err)
	}
	next, err := s.ChangeBatch(ctx, first.ID, UserB, "batch")
	if err != nil {
		t.Fatal(err)
	}
	if next.SessionID != first.SessionID || next.ID == first.ID {
		t.Fatal("wrong batch identity")
	}
	for _, o := range next.Result.Options {
		for _, old := range first.Result.Options {
			if o.Selection.NoteID == old.Selection.NoteID {
				t.Fatal("unseen notes available but repeated old batch")
			}
		}
	}
	retry, err := s.ChangeBatch(ctx, first.ID, UserB, "batch")
	if err != nil || retry.ID != next.ID {
		t.Fatal("retry not idempotent", err)
	}
	if _, err := s.ChangeBatch(ctx, first.ID, UserA, "batch"); err == nil {
		t.Fatal("command actor changed")
	}
	if _, err := s.ChangeBatch(ctx, first.ID, UserB, "stale"); err == nil {
		t.Fatal("old batch changed again")
	}
	state, err = s.GetSession(first.SessionID, UserB)
	if err != nil || state.AdoptedOption == nil || state.AdoptedOption.DecisionID != first.ID {
		t.Fatal("batch lost adoption", err)
	}
	state = feedback(t, s, first, "readopt", decisions.FeedbackAdopt, 1, UserB)
	if slices.Contains(state.ExcludedNoteIDs, rejected) {
		t.Fatal("readoption kept exclusion")
	}
	feedback(t, s, next, "newer", decisions.FeedbackAdopt, 0, UserB)
	state = feedback(t, s, first, "adopt", decisions.FeedbackAdopt, 0, UserA)
	if state.AdoptedOption.DecisionID != next.ID {
		t.Fatal("retry overwrote newer adoption")
	}
	changed := request
	limit := int64(20000)
	changed.Conditions.BudgetMaxCents = &limit
	changed.SessionID = first.SessionID
	if _, err := s.Generate(ctx, changed, UserA); err == nil {
		t.Fatal("new conditions reused session")
	}
	changed.SessionID = ""
	fresh, err := s.Generate(ctx, changed, UserA)
	if err != nil || fresh.SessionID == first.SessionID {
		t.Fatal(err)
	}
	state, err = s.GetSession(fresh.SessionID, UserA)
	if err != nil || len(state.ExcludedNoteIDs) != 0 || state.AdoptedOption != nil {
		t.Fatal("old state inherited", err)
	}
}

func TestFailedBatchCanResumeWithoutLosingOldResult(t *testing.T) {
	f := NewFixture()
	calls := 0
	p := providerFunc(func(ctx context.Context, input decisions.Context) (decisions.Result, error) {
		calls++
		if calls == 2 {
			return decisions.Result{}, errors.New("simulated timeout")
		}
		return (FixedProvider{}).Generate(ctx, input)
	})
	s := serviceFor(t, f, p, nil)
	ctx := context.Background()
	first, err := s.Generate(ctx, requestFor(t, f, "normal"), UserA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ChangeBatch(ctx, first.ID, UserB, "retry"); err == nil {
		t.Fatal("timeout hidden")
	}
	state, err := s.GetSession(first.SessionID, UserA)
	if err != nil || state.LatestDecisionID != first.ID || len(state.RecommendedNoteIDs) != 3 {
		t.Fatal("failed generation overwrote state", err)
	}
	next, err := s.ChangeBatch(ctx, first.ID, UserB, "retry")
	if err != nil || next.ID == first.ID {
		t.Fatal("could not resume", err)
	}
	history, err := s.GetDecision(first.ID, UserA)
	if err != nil || len(history.Feedback) != 1 {
		t.Fatal("duplicated command event", err)
	}
	retry, err := s.ChangeBatch(ctx, first.ID, UserB, "retry")
	if err != nil || retry.ID != next.ID || calls != 3 {
		t.Fatal("completed retry reran provider", err)
	}
}

func TestSnapshotsCancellationAndPlanningRejection(t *testing.T) {
	f := NewFixture()
	s := serviceFor(t, f, FixedProvider{}, nil)
	ctx := context.Background()
	first, err := s.Generate(ctx, requestFor(t, f, "normal"), UserA)
	if err != nil {
		t.Fatal(err)
	}
	first.Result.Options[0].Selection.NoteID = "tampered"
	first.CandidateNotes[0].Tags[0] = "tampered"
	saved, err := s.GetDecision(first.ID, UserA)
	if err != nil || saved.Result.Options[0].Selection.NoteID == "tampered" || saved.CandidateNotes[0].Tags[0] == "tampered" {
		t.Fatal("returned snapshot aliases store", err)
	}
	if _, err := s.GetDecision(first.ID, "outsider"); !errors.Is(err, decisions.ErrForbidden) {
		t.Fatal("history leaked", err)
	}
	request := requestFor(t, f, "normal")
	request.Task = decisions.TaskDayPlan
	if _, err := s.Generate(ctx, request, UserA); err == nil {
		t.Fatal("planning executed")
	}
	request.Task = decisions.TaskSelect
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.Generate(cancelled, request, UserA); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled generation persisted", err)
	}
}

func TestExpiryAndAdoptionRechecksFacts(t *testing.T) {
	f := NewFixture()
	now := time.Now()
	s := serviceFor(t, f, FixedProvider{}, func() time.Time { return now })
	ctx := context.Background()
	d, err := s.Generate(ctx, requestFor(t, f, "normal"), UserA)
	if err != nil {
		t.Fatal(err)
	}
	id := d.Result.Options[0].Selection.NoteID
	fact := f.Facts[id]
	fact.IngredientsKnown = false
	f.Facts[id] = fact
	event := decisions.Feedback{ID: "adopt", DecisionID: d.ID, Action: decisions.FeedbackAdopt, OptionID: d.Result.Options[0].OptionID}
	if _, err := s.Feedback(ctx, event, UserA); err == nil {
		t.Fatal("unknown hard constraint adopted")
	}
	fact.IngredientsKnown = true
	f.Facts[id] = fact
	state := feedback(t, s, d, "adopt", decisions.FeedbackAdopt, 0, UserA)
	now = state.ExpiresAt
	state = feedback(t, s, d, "expired", decisions.FeedbackReject, 0, UserB)
	if state.AdoptedOption == nil || len(state.ExcludedNoteIDs) != 0 {
		t.Fatal("expired projection changed")
	}
	history, err := s.GetDecision(d.ID, UserA)
	if err != nil || history.AdoptedOptionID() != "" {
		t.Fatal("expired feedback not recorded", err)
	}
	if _, err := s.ChangeBatch(ctx, d.ID, UserB, "expired-batch"); err == nil {
		t.Fatal("expired session reused")
	}
}

func TestShortBatchAndExhaustion(t *testing.T) {
	f := NewFixture()
	s := serviceFor(t, f, FixedProvider{}, nil)
	ctx := context.Background()
	first, err := s.Generate(ctx, requestFor(t, f, "short"), UserA)
	if err != nil || len(first.Result.Options) != 3 {
		t.Fatal(err)
	}
	second, err := s.ChangeBatch(ctx, first.ID, UserB, "short")
	if err != nil || len(second.Result.Options) != 2 {
		t.Fatal("short batch fabricated repeats", err)
	}
	for i, d := range []decisions.Decision{first, second} {
		for j := range d.Result.Options {
			feedback(t, s, d, fmt.Sprintf("reject-%d-%d", i, j), decisions.FeedbackReject, j, UserA)
		}
	}
	last, err := s.ChangeBatch(ctx, second.ID, UserB, "empty")
	if err != nil || last.Result.Outcome != decisions.OutcomeNoCandidates {
		t.Fatal("excluded candidates resurrected", err)
	}
}

func TestInvalidModelAndIncompleteEvaluation(t *testing.T) {
	f := NewFixture()
	p := providerFunc(func(ctx context.Context, input decisions.Context) (decisions.Result, error) {
		r, err := (FixedProvider{}).Generate(ctx, input)
		if err != nil {
			return r, err
		}
		r.Options[0].Selection.NoteID = "foreign"
		return r, nil
	})
	s := serviceFor(t, f, p, nil)
	if _, err := s.Generate(context.Background(), requestFor(t, f, "normal"), UserA); err == nil {
		t.Fatal("foreign model note accepted")
	}
	store, err := decisions.NewMemoryStore(f.Dataset)
	if err != nil {
		t.Fatal(err)
	}
	checker := checkerFunc(func(context.Context, decisions.Context) (decisions.SelectEvaluation, error) {
		return decisions.SelectEvaluation{}, nil
	})
	s, err = decisions.NewDecisionService(store, checker, FixedProvider{}, decisions.ServiceConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Generate(context.Background(), requestFor(t, f, "normal"), UserA); err == nil {
		t.Fatal("incomplete evaluation passed")
	}
}

func TestLateGenerationDoesNotOverwriteFeedback(t *testing.T) {
	f := NewFixture()
	entered, release := make(chan struct{}), make(chan struct{})
	calls := 0
	p := providerFunc(func(ctx context.Context, input decisions.Context) (decisions.Result, error) {
		calls++
		if calls == 2 {
			close(entered)
			<-release
		}
		return (FixedProvider{}).Generate(ctx, input)
	})
	s := serviceFor(t, f, p, nil)
	ctx := context.Background()
	first, err := s.Generate(ctx, requestFor(t, f, "normal"), UserA)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := s.ChangeBatch(ctx, first.ID, UserB, "late"); done <- err }()
	<-entered
	state, feedbackErr := s.Feedback(ctx, decisions.Feedback{ID: "meanwhile", DecisionID: first.ID, Action: decisions.FeedbackReject, OptionID: first.Result.Options[0].OptionID}, UserA)
	close(release)
	if feedbackErr != nil {
		t.Fatal(feedbackErr)
	}
	if err := <-done; !errors.Is(err, decisions.ErrStateChanged) {
		t.Fatal("late result committed", err)
	}
	saved, err := s.GetSession(first.SessionID, UserA)
	if err != nil || saved.LatestDecisionID != first.ID || !slices.Equal(saved.ExcludedNoteIDs, state.ExcludedNoteIDs) {
		t.Fatal("feedback lost", err)
	}
	if _, err := s.ChangeBatch(ctx, first.ID, UserB, "late"); err != nil {
		t.Fatal("late command not retryable", err)
	}
}
