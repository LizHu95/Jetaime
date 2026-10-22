package intervention

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
)

type sequenceModel struct {
	outputs  []string
	requests []llm.Request
}

func (m *sequenceModel) Call(_ context.Context, request llm.Request) (llm.Result, error) {
	m.requests = append(m.requests, request)
	if len(m.outputs) == 0 {
		return llm.Result{}, errors.New("unexpected model call")
	}
	output := m.outputs[0]
	m.outputs = m.outputs[1:]
	return llm.Result{Content: output}, nil
}
func window() ai.RecognitionInput {
	return ai.RecognitionInput{TriggerMode: "proactive", CurrentMessageID: "current", Messages: []ai.Message{{ID: "current", Text: "按之前预算订500元这家", SentAt: time.Now()}}}
}
func verification() ai.VerificationInput {
	return ai.VerificationInput{Conversation: window(), Candidate: ai.InterventionCandidate{Action: "verify", Reason: "缺少此前预算", Breakdowns: []ai.Breakdown{}, Verification: &ai.VerificationQuestion{Topic: "本次聚餐", Query: "预算及后续变更", EvidenceMessageIDs: []string{"current"}}}, Evidence: ai.InterventionEvidence{Coverage: "partial", Messages: []ai.Message{{ID: "old", Text: "本次人均预算300", SentAt: time.Now().Add(-time.Hour)}}}}
}
func TestDiscoveryRejectsInvalidTransitionsAndEvidence(t *testing.T) {
	for _, output := range []string{
		`{"action":"verify","reason":"核验","breakdowns":[],"intent":null,"verification":null}`,
		`{"action":"verify","reason":"核验","breakdowns":[],"intent":null,"verification":{"topic":"聚餐","query":"预算","evidenceMessageIds":["foreign"]}}`,
		`{"action":"intervene","reason":"已知冲突","breakdowns":[{"type":"forgotten_constraint","evidenceMessageIds":["current"]}],"intent":{"type":"clarify","objective":"确认预算","evidenceMessageIds":["current"]},"verification":null}`,
		`{"action":"skip","reason":"无需参与","breakdowns":[],"intent":{"type":"clarify","objective":"仍要参与","evidenceMessageIds":["current"]},"verification":null}`,
	} {
		model := &sequenceModel{outputs: []string{output}}
		judge, _ := New(model, "test", 32768)
		_, err := judge.Discover(context.Background(), window())
		if !errors.Is(err, ai.ErrInvalidDecision) || len(model.requests) != 1 {
			t.Fatalf("semantic failure was repaired or accepted: %v calls=%d", err, len(model.requests))
		}
	}
}
func TestFinalDecisionRequiresBothSourcesAndCannotAuthorizeWrites(t *testing.T) {
	for _, output := range []string{
		`{"shouldIntervene":true,"reason":"历史操作","breakdowns":[],"intent":{"type":"execute_request","objective":"创建提醒","evidenceMessageIds":["old","current"]}}`,
		`{"shouldIntervene":true,"reason":"缺少新证据","breakdowns":[],"intent":{"type":"clarify","objective":"确认预算","evidenceMessageIds":["current"]}}`,
		`{"shouldIntervene":true,"reason":"缺少当前依据","breakdowns":[],"intent":{"type":"clarify","objective":"确认预算","evidenceMessageIds":["old"]}}`,
		`{"shouldIntervene":true,"reason":"伪造证据","breakdowns":[],"intent":{"type":"clarify","objective":"确认预算","evidenceMessageIds":["foreign","current"]}}`,
	} {
		model := &sequenceModel{outputs: []string{output}}
		judge, _ := New(model, "test", 32768)
		_, err := judge.Decide(context.Background(), verification())
		if !errors.Is(err, ai.ErrInvalidDecision) || len(model.requests) != 1 {
			t.Fatalf("unsafe final decision accepted/repaired: %v calls=%d", err, len(model.requests))
		}
	}
}
func TestFinalDecisionRepairsFormatOnce(t *testing.T) {
	model := &sequenceModel{outputs: []string{
		`{"shouldIntervene":true,"reason":"需确认","breakdowns":[],"intent":{"type":"clarify","evidenceMessageIds":["old","current"]},"objective":"确认本次预算"}`,
		`{"shouldIntervene":true,"reason":"需确认","breakdowns":[],"intent":{"type":"clarify","objective":"确认本次预算","evidenceMessageIds":["old","current"]}}`,
	}}
	judge, _ := New(model, "test", 32768)
	result, err := judge.Decide(context.Background(), verification())
	if err != nil || !result.Decision.ShouldIntervene || len(result.Steps) != 2 || result.Steps[0].Status != "failed" || result.Steps[1].Name != "decide_intervention" {
		t.Fatal("final repair failed", result, err)
	}
}
func TestDiscoveryBudgetFailsBeforeInference(t *testing.T) {
	model := &sequenceModel{}
	judge, _ := New(model, "test", 4096)
	input := window()
	input.Messages[0].Text = string(make([]byte, 10000))
	_, err := judge.Discover(context.Background(), input)
	if !errors.Is(err, ai.ErrContext) || len(model.requests) != 0 {
		t.Fatal("over-budget discovery called model", err)
	}
}

func TestFinalDecisionBudgetFailsBeforeInference(t *testing.T) {
	model := &sequenceModel{}
	judge, _ := New(model, "test", 32768)
	input := verification()
	input.Evidence.Messages[0].Text = strings.Repeat("历史原文", 16384)
	_, err := judge.Decide(context.Background(), input)
	if !errors.Is(err, ai.ErrContext) || len(model.requests) != 0 {
		t.Fatal("over-budget verification called model", err)
	}
}
