package intervention

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func unsupportedCommunicationCandidate() (ai.RecognitionInput, ai.InterventionCandidate) {
	input := ai.RecognitionInput{TriggerMode: "proactive", CurrentMessageID: "sad", Messages: []ai.Message{
		{ID: "broken", SenderID: "alice", SenderType: "user", Text: "你把我的手办弄坏了"},
		{ID: "sad", SenderID: "alice", SenderType: "user", Text: "我很难过"},
	}}
	ids := []string{"broken", "sad"}
	decision := ai.InterventionCandidate{
		TopicContext: &ai.TopicContext{Topic: "手办损坏", Continuity: "new_topic", MessageIDs: ids, AnchorMessageIDs: ids},
		Action:       "intervene", Reason: "难过需要情绪支持", Breakdowns: []ai.Breakdown{{Type: "support_mismatch", EvidenceMessageIDs: ids}},
		Intent: &ai.InterventionIntent{Type: "suggest", Objective: "安抚情绪并建议如何沟通", EvidenceMessageIDs: ids},
	}
	return input, decision
}

func TestInsufficientParticipantEvidenceIsSuccessfulSilence(t *testing.T) {
	// Check the exported Phoenix-facing span status, not just the Go return value.
	previous := otel.GetTracerProvider()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(context.Background())
	})
	input, candidate := unsupportedCommunicationCandidate()
	raw, _ := json.Marshal(candidate)
	model := &sequenceModel{outputs: []string{string(raw)}}
	judge, _ := New(model, "test", 32768)
	result, err := judge.Discover(context.Background(), input)
	if err != nil || result.Decision.Action != "skip" || result.Decision.Intent != nil || len(result.Decision.Breakdowns) != 0 || len(model.requests) != 1 {
		t.Fatalf("rejected candidate did not end silently: %+v %v", result, err)
	}
	if len(result.Steps) != 1 || result.Steps[0].Status != "succeeded" || result.Steps[0].Error != "" {
		t.Fatalf("ordinary rejection marked failed: %+v", result.Steps)
	}
	diagnostic := result.Steps[0].Output.(map[string]any)
	if diagnostic["outcome"] != "candidate_rejected" || diagnostic["reasonCode"] != "insufficient_participant_evidence" || diagnostic["modelOutput"] != string(raw) || diagnostic["reason"] == "" {
		t.Fatalf("rejection diagnostic lost: %+v", diagnostic)
	}
	spans := recorder.Ended()
	if len(spans) != 1 || spans[0].Name() != "step.discover_intervention" || spans[0].Status().Code != codes.Ok {
		t.Fatalf("evidence rejection exported as a workflow error: %+v", spans)
	}
}

func TestEvidenceRejectionCannotMaskInvalidDecision(t *testing.T) {
	for _, scenario := range []string{"foreign_intent_source", "second_breakdown_foreign_source", "write_without_current_source", "invalid_transition", "assistant_source"} {
		t.Run(scenario, func(t *testing.T) {
			input, candidate := unsupportedCommunicationCandidate()
			switch scenario {
			case "foreign_intent_source":
				candidate.Intent.EvidenceMessageIDs = []string{"foreign"}
			case "second_breakdown_foreign_source":
				candidate.Breakdowns = append(candidate.Breakdowns, ai.Breakdown{Type: "uncertainty", EvidenceMessageIDs: []string{"foreign"}})
			case "write_without_current_source":
				candidate.Intent.Type = "execute_request"
				candidate.Intent.EvidenceMessageIDs = []string{"broken"}
			case "invalid_transition":
				candidate.Verification = &ai.VerificationQuestion{Topic: "手办", Query: "此前约定是什么", EvidenceMessageIDs: []string{"sad"}}
			case "assistant_source":
				input.Messages[0].SenderType = "assistant"
			}
			raw, _ := json.Marshal(candidate)
			model := &sequenceModel{outputs: []string{string(raw)}}
			judge, _ := New(model, "test", 32768)
			result, err := judge.Discover(context.Background(), input)
			if !errors.Is(err, ai.ErrInvalidDecision) || len(result.Steps) != 1 || result.Steps[0].Status != "failed" {
				t.Fatalf("invalid decision hidden as normal silence: %+v %v", result, err)
			}
		})
	}
}

func TestVerificationEvidenceRejectionStillChecksAuthority(t *testing.T) {
	for _, scenario := range []string{"insufficient_participants", "write", "missing_history"} {
		t.Run(scenario, func(t *testing.T) {
			input := verification()
			input.Conversation.Messages[0].SenderID = "alice"
			input.Evidence.Messages[0].SenderID = "alice"
			ids := []string{"old", "current"}
			decision := ai.Recognition{ShouldIntervene: true, Reason: "需要沟通帮助", Breakdowns: []ai.Breakdown{{Type: "support_mismatch", EvidenceMessageIDs: ids}}, Intent: &ai.InterventionIntent{Type: "suggest", Objective: "接住支持需要", EvidenceMessageIDs: ids}}
			if scenario == "write" {
				decision.Intent.Type = "execute_request"
			}
			if scenario == "missing_history" {
				decision.Intent.EvidenceMessageIDs = []string{"current"}
			}
			raw, _ := json.Marshal(decision)
			model := &sequenceModel{outputs: []string{string(raw)}}
			if scenario == "write" {
				// The verification schema now rejects writes before semantic checks,
				// so exercise both attempts of the bounded structural repair.
				model.outputs = append(model.outputs, string(raw))
			}
			judge, _ := New(model, "test", 32768)
			result, err := judge.Decide(context.Background(), input)
			if scenario == "insufficient_participants" {
				if err != nil || result.Decision.ShouldIntervene || result.Decision.Intent != nil || result.Steps[0].Status != "succeeded" {
					t.Fatalf("expected successful silence: %+v %v", result, err)
				}
			} else if !errors.Is(err, ai.ErrInvalidDecision) || result.Steps[0].Status != "failed" {
				t.Fatalf("verification authorization failure hidden: %+v %v", result, err)
			}
		})
	}
}
