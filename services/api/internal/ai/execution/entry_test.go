package execution

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
)

type testAgent struct {
	calls int
	input ai.Invocation
	err   error
}

func (a *testAgent) ID() string { return "main" }
func (a *testAgent) Invoke(_ context.Context, in ai.Invocation) (ai.Run, error) {
	a.calls++
	a.input = in
	return ai.Run{Kind: "main", Steps: []ai.Step{{Name: "execute"}}}, a.err
}

type testRecognizer struct {
	calls       int
	input       ai.RecognitionInput
	result      ai.RecognitionResult
	err         error
	cancel      context.CancelFunc
	candidate   *ai.InterventionCandidate
	final       ai.RecognitionResult
	finalErr    error
	decideCalls int
	finalInput  ai.VerificationInput
}

func (r *testRecognizer) Discover(_ context.Context, in ai.RecognitionInput) (ai.CandidateResult, error) {
	r.calls++
	r.input = in
	if r.cancel != nil {
		r.cancel()
	}
	candidate := ai.InterventionCandidate{Action: "skip", Reason: r.result.Decision.Reason, Breakdowns: r.result.Decision.Breakdowns, Intent: r.result.Decision.Intent}
	if r.result.Decision.ShouldIntervene {
		candidate.Action = "intervene"
	}
	if r.candidate != nil {
		candidate = *r.candidate
	}
	if candidate.TopicContext == nil && candidate.Action != "skip" {
		ids := []string{}
		for _, message := range in.Messages {
			ids = append(ids, message.ID)
		}
		candidate.TopicContext = &ai.TopicContext{Topic: "当前测试事项", Continuity: "continuation", MessageIDs: ids, AnchorMessageIDs: []string{in.CurrentMessageID}}
	}
	return ai.CandidateResult{Decision: candidate, Steps: r.result.Steps, Model: r.result.Model, PromptVersion: r.result.PromptVersion}, r.err
}
func (r *testRecognizer) Decide(_ context.Context, in ai.VerificationInput) (ai.RecognitionResult, error) {
	r.decideCalls++
	r.finalInput = in
	return r.final, r.finalErr
}
func passiveInput() ai.Invocation {
	return ai.Invocation{TriggerMode: "proactive", ContextEndSeq: 42, Mode: "auto", Question: "普通聊天", Context: ai.Context{
		GroupID: "g", ActorID: "u", Users: []ai.User{{ID: "u", Nickname: "用户"}},
		Messages: []ai.Message{
			{ID: "m1", GroupID: "g", SenderID: "u", SentAt: time.Now(), Text: "周末去哪儿"},
			{ID: "m2", GroupID: "g", SenderID: "u", SentAt: time.Now(), Text: "还没想好"},
		}, Request: &ai.RequestContext{MessageID: "m2", RelatedMessageIDs: []string{"m1"}},
	}}
}
func TestActiveRequestsBypassRecognition(t *testing.T) {
	for _, mode := range []string{"", "mention"} {
		t.Run(mode, func(t *testing.T) {
			main, judge := &testAgent{}, &testRecognizer{err: errors.New("must not judge")}
			entry, err := newEntry(main, judge)
			if err != nil {
				t.Fatal(err)
			}
			in := passiveInput()
			in.TriggerMode = mode
			in.Mode = "help"
			_, err = entry.Invoke(context.Background(), in)
			if err != nil || judge.calls != 0 || main.calls != 1 || !reflect.DeepEqual(in, main.input) {
				t.Fatalf("active request did not go directly to main: err=%v judge=%d main=%d", err, judge.calls, main.calls)
			}
		})
	}
}
func TestPassiveSilenceAndFailureNeverInvokeMain(t *testing.T) {
	failed := errors.New("recognition failed")
	for _, failure := range []error{nil, failed} {
		main := &testAgent{}
		judge := &testRecognizer{result: ai.RecognitionResult{Model: "judge-model", PromptVersion: "judge-v1", Steps: []ai.Step{{Name: "recognize_intent"}}}, err: failure}
		entry, _ := newEntry(main, judge)
		out, err := entry.Invoke(context.Background(), passiveInput())
		if !errors.Is(err, failure) || main.calls != 0 || judge.calls != 1 || out.Model != "judge-model" || len(out.Steps) != 1 || out.StartedAt.IsZero() {
			t.Fatalf("passive gate failed: %+v err=%v calls=%d", out, err, main.calls)
		}
		if failure == nil && (!out.Silent || out.Status != "succeeded" || out.Recognition == nil) {
			t.Fatal("successful silence was not retained", out)
		}
		if failure != nil && (out.Status != "failed" || out.Recognition != nil) {
			t.Fatal("recognition failure became a success", out)
		}
	}
}
func TestPassiveInterventionUsesSharedMainWithEvidence(t *testing.T) {
	for _, intent := range []string{"clarify", "execute_request"} {
		main := &testAgent{}
		judge := &testRecognizer{result: ai.RecognitionResult{Decision: ai.Recognition{
			ShouldIntervene: true, Intent: &ai.InterventionIntent{Type: intent, Objective: "确认地点", EvidenceMessageIDs: []string{"m2"}},
			Breakdowns: []ai.Breakdown{{Type: "uncertainty", EvidenceMessageIDs: []string{"m1"}}},
		}, Steps: []ai.Step{{Name: "recognize_intent"}}}}
		entry, _ := newEntry(main, judge)
		in := passiveInput()
		in.Mode = "help" // Passive execution must still enter the shared main loop.
		in.LoadExtraction = func(context.Context) (ai.Context, error) { return in.Context, nil }
		out, err := entry.Invoke(context.Background(), in)
		if err != nil || main.calls != 1 || judge.calls != 1 || !reflect.DeepEqual(judge.input.Messages, in.Context.Messages) {
			t.Fatal("wrong entry path", err)
		}
		request := main.input.Context.Request
		if main.input.Mode != "main" || main.input.ContextEndSeq != 42 || main.input.LoadExtraction == nil || !strings.Contains(main.input.Question, "确认地点") || request.TriggerMode != "proactive" || request.AllowWrites != (intent == "execute_request") || !reflect.DeepEqual(request.RelatedMessageIDs, []string{"m1", "m2"}) {
			t.Fatalf("lost execution context: %+v request=%+v", main.input, request)
		}
		if in.Context.Request == request || in.Context.Request.AllowWrites || in.Context.Request.TriggerMode != "" || len(in.Context.Request.RelatedMessageIDs) != 1 {
			t.Fatal("mutated recognition snapshot")
		}
		if out.Recognition == nil || len(out.Steps) != 2 || out.Steps[0].Name != "recognize_intent" || out.Steps[1].Name != "execute" {
			t.Fatal("recognition and execution traces not joined", out)
		}
	}
}

func TestCommunicationSkillRespectsPrimaryGoalAndExplicitOperations(t *testing.T) {
	for _, tc := range []struct {
		name, intent string
		kinds        []string
		wantHelp     bool
	}{
		{"primary communication", "clarify", []string{"pacing_conflict", "conflict"}, true},
		{"secondary communication", "clarify", []string{"conflict", "pacing_conflict"}, false},
		{"explicit operation", "execute_request", []string{"pacing_conflict"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			breakdowns := []ai.Breakdown{}
			for _, kind := range tc.kinds {
				breakdowns = append(breakdowns, ai.Breakdown{Type: kind, EvidenceMessageIDs: []string{"m1", "m2"}})
			}
			main := &testAgent{}
			judge := &testRecognizer{result: ai.RecognitionResult{Decision: ai.Recognition{ShouldIntervene: true, Reason: "当前目标明确", Breakdowns: breakdowns, Intent: &ai.InterventionIntent{Type: tc.intent, Objective: "处理当前目标", EvidenceMessageIDs: []string{"m1", "m2"}}}}}
			entry, _ := newEntry(main, judge)
			out, err := entry.Invoke(context.Background(), passiveInput())
			if err != nil || main.calls != 1 {
				t.Fatalf("main not invoked: %v", err)
			}
			ref := main.input.Context.Request.Skill
			if (ref != nil) != tc.wantHelp || (ref != nil && (ref.ID != "negotiate-pause" || out.Skill == nil || *out.Skill != *ref)) {
				t.Fatalf("skill overrode primary goal: %+v", ref)
			}
			if strings.Contains(main.input.Question, "negotiate-pause") {
				t.Fatal("skill instructions leaked into the user question")
			}
		})
	}
}
func TestCancellationAfterRecognitionDoesNotInvokeMain(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	main := &testAgent{}
	judge := &testRecognizer{cancel: cancel, result: ai.RecognitionResult{Decision: ai.Recognition{ShouldIntervene: true, Intent: &ai.InterventionIntent{Type: "clarify"}}}}
	entry, _ := newEntry(main, judge)
	_, err := entry.Invoke(ctx, passiveInput())
	if !errors.Is(err, context.Canceled) || main.calls != 0 {
		t.Fatal("cancelled passive request entered main", err)
	}
}
func TestInvalidPassiveWindowDoesNotReachRecognizer(t *testing.T) {
	for _, mutate := range []func(*ai.Invocation){
		func(in *ai.Invocation) { in.Context.Request = nil },
		func(in *ai.Invocation) { in.Context.Messages = nil },
		func(in *ai.Invocation) { in.Context.Request.MessageID = "unknown" },
		func(in *ai.Invocation) { in.Context.Messages[1].SenderType = "assistant" },
	} {
		main, judge := &testAgent{}, &testRecognizer{}
		entry, _ := newEntry(main, judge)
		in := passiveInput()
		mutate(&in)
		_, err := entry.Invoke(context.Background(), in)
		if !errors.Is(err, ai.ErrInvalidDecision) || main.calls != 0 || judge.calls != 0 {
			t.Fatal("invalid passive input reached inference", err)
		}
	}
}

func verifyCandidate() *ai.InterventionCandidate {
	return &ai.InterventionCandidate{Action: "verify", Reason: "核对本次预算", Breakdowns: []ai.Breakdown{}, Verification: &ai.VerificationQuestion{Topic: "本次聚餐", Query: "预算是否修改", EvidenceMessageIDs: []string{"m2"}}}
}

func TestVerificationCannotBypassEvidenceGate(t *testing.T) {
	failure := errors.New("history unavailable")
	for _, tc := range []struct {
		name                                                          string
		noProvider, noFence, empty, foreign, future, large, cancelled bool
		providerErr, finalErr                                         error
		wantErr                                                       bool
	}{
		{name: "no provider", noProvider: true},
		{name: "no trusted fence", noFence: true},
		{name: "no new sources", empty: true},
		{name: "failed read", providerErr: failure, wantErr: true},
		{name: "foreign evidence", foreign: true, wantErr: true},
		{name: "future evidence", future: true, wantErr: true},
		{name: "large evidence reaches final decision", large: true},
		{name: "cancelled before read", cancelled: true, wantErr: true},
		{name: "failed final decision", finalErr: failure, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := passiveInput()
			main := &testAgent{}
			judge := &testRecognizer{candidate: verifyCandidate(), finalErr: tc.finalErr}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancelled {
				judge.cancel = cancel
			}
			calls := 0
			provider := ai.InterventionEvidenceProvider(evidenceProviderFunc(func(ctx context.Context, r ai.InterventionEvidenceRequest) (ai.InterventionEvidence, error) {
				calls++
				if r.ContextEndSeq != 42 || r.Context.GroupID != "g" || r.Context.ActorID != "u" {
					t.Fatal("scope lost", r)
				}
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("unbounded evidence read")
				}
				out := ai.InterventionEvidence{Coverage: "partial", Messages: []ai.Message{{ID: "old", GroupID: "g", SenderID: "u", SentAt: in.Context.Messages[0].SentAt.Add(-time.Hour), Text: "预算300元"}}}
				if tc.empty {
					out.Messages = nil
				}
				if tc.foreign {
					out.Messages[0].GroupID = "other"
				}
				if tc.future {
					out.Messages[0].SentAt = time.Now().Add(time.Hour)
				}
				if tc.large {
					out.Messages[0].Text = strings.Repeat("a", 16*1024)
				}
				return out, tc.providerErr
			}))
			if tc.noProvider {
				provider = nil
			}
			if tc.noFence {
				in.ContextEndSeq = 0
			}
			entry, _ := newEntry(main, judge, provider)
			out, err := entry.Invoke(ctx, in)
			if (err != nil) != tc.wantErr || main.calls != 0 {
				t.Fatalf("verification gate failed: err=%v main=%d", err, main.calls)
			}
			if !tc.wantErr && (!out.Silent || out.Recognition == nil) {
				t.Fatal("missing evidence did not end silently", out)
			}
			if tc.noProvider || tc.noFence || tc.cancelled {
				if calls != 0 || judge.decideCalls != 0 {
					t.Fatal("unavailable/cancelled request still read history")
				}
			} else if tc.empty || tc.foreign || tc.future || tc.providerErr != nil {
				if calls != 1 || judge.decideCalls != 0 {
					t.Fatal("bad evidence reached final decision")
				}
			} else if calls != 1 || judge.decideCalls != 1 {
				t.Fatal("verification repeated unexpectedly")
			}
			if tc.large && len(judge.finalInput.Evidence.Messages[0].Text) != 16*1024 {
				t.Fatal("long evidence changed before final model budget check")
			}
		})
	}
}

// evidenceProviderFunc keeps callback-based fixtures local to tests.
type evidenceProviderFunc func(context.Context, ai.InterventionEvidenceRequest) (ai.InterventionEvidence, error)

func (f evidenceProviderFunc) Retrieve(ctx context.Context, request ai.InterventionEvidenceRequest) (ai.InterventionEvidence, error) {
	return f(ctx, request)
}
