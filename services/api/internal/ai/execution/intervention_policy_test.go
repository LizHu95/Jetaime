package execution

import (
	"context"
	"errors"
	"testing"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
)

func TestPassiveInterventionPolicyBeforeMain(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		checkErr     error
		explicit     bool
		wantCalls    int
	}{
		{name: "allowed", wantCalls: 1},
		{name: "cooldown", reason: "cooldown"},
		{name: "duplicate", reason: "duplicate_topic"},
		{name: "policy failed", checkErr: errors.New("database unavailable")},
		{name: "explicit request", reason: "cooldown", explicit: true, wantCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			intent := "clarify"
			if tc.explicit {
				intent = "execute_request"
			}
			judge := &testRecognizer{result: ai.RecognitionResult{Decision: ai.Recognition{ShouldIntervene: true, Reason: "需要帮助", Breakdowns: []ai.Breakdown{{Type: "pacing_conflict", EvidenceMessageIDs: []string{"m1", "m2"}}}, Intent: &ai.InterventionIntent{Type: intent, Objective: "协调暂停", EvidenceMessageIDs: []string{"m2"}}}}}
			main := &testAgent{}
			entry, err := newEntry(main, judge)
			if err != nil {
				t.Fatal(err)
			}
			input := passiveInput()
			checks := 0
			input.CheckIntervention = func(_ context.Context, topic string, decision ai.Recognition) (string, error) {
				checks++
				if topic != "当前测试事项" || !decision.ShouldIntervene || decision.Intent.Type != "clarify" {
					t.Fatal("policy did not receive recognized topic/intent")
				}
				return tc.reason, tc.checkErr
			}
			out, err := entry.Invoke(context.Background(), input)
			if !errors.Is(err, tc.checkErr) || main.calls != tc.wantCalls || out.InterventionTopic != "当前测试事项" {
				t.Fatalf("policy routing failed: main=%d out=%+v err=%v", main.calls, out, err)
			}
			if tc.explicit {
				if checks != 0 || out.Skill != nil {
					t.Fatal("explicit request entered unsolicited-help gate")
				}
				return
			}
			if checks != 1 {
				t.Fatal("policy check not called exactly once")
			}
			if tc.reason != "" && (out.Recognition == nil || out.Recognition.ShouldIntervene || !out.Silent || out.Status != "succeeded") {
				t.Fatal("suppression did not produce successful silence", out)
			}
			if (tc.reason != "" || tc.checkErr != nil) && out.Skill != nil {
				t.Fatal("suppressed or failed intervention loaded a skill")
			}
			if tc.checkErr != nil && out.Silent {
				t.Fatal("database failure concealed as normal silence")
			}
		})
	}
}

func TestSilentAndActiveRequestsBypassInterventionPolicy(t *testing.T) {
	for _, mode := range []string{"mention", "proactive"} {
		main, judge := &testAgent{}, &testRecognizer{}
		entry, err := newEntry(main, judge)
		if err != nil {
			t.Fatal(err)
		}
		input := passiveInput()
		input.TriggerMode = mode
		input.CheckIntervention = func(context.Context, string, ai.Recognition) (string, error) {
			t.Fatal("silence or active request entered policy gate")
			return "", nil
		}
		if _, err := entry.Invoke(context.Background(), input); err != nil {
			t.Fatal(err)
		}
	}
}
