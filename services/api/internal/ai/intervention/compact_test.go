package intervention

import (
	"context"
	"errors"
	"testing"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
)

func TestCompactDiscoveryBranches(t *testing.T) {
	for _, tc := range []struct{ name, output, action string }{
		{"skip", `{"action":"skip","reason":"正常闲聊"}`, "skip"},
		{"intervene", `{"action":"intervene","reason":"明确请求","topicContext":{"topic":"查询","continuity":"new_topic","messageIds":["current"],"anchorMessageIds":["current"]},"intent":{"type":"answer","objective":"回答当前问题","evidenceMessageIds":["current"]}}`, "intervene"},
		{"verify", `{"action":"verify","reason":"需核实此前预算","topicContext":{"topic":"聚餐","continuity":"continuation","messageIds":["current"],"anchorMessageIds":["current"]},"verification":{"topic":"本次聚餐","query":"预算及后续修改是什么","evidenceMessageIds":["current"]}}`, "verify"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := &sequenceModel{outputs: []string{tc.output}}
			judge, err := New(model, "test", 32768)
			if err != nil {
				t.Fatal(err)
			}
			result, err := judge.Discover(context.Background(), window())
			if err != nil || result.Decision.Action != tc.action || result.Decision.Breakdowns == nil || len(model.requests) != 1 {
				t.Fatalf("compact branch failed: %+v calls=%d err=%v", result, len(model.requests), err)
			}
			if tc.action == "skip" && (result.Decision.TopicContext != nil || result.Decision.Intent != nil || result.Decision.Verification != nil) {
				t.Fatal("skip acquired an intent or evidence")
			}
		})
	}
}

func TestCompactVerificationBranches(t *testing.T) {
	for _, tc := range []struct {
		output    string
		intervene bool
	}{
		{`{"shouldIntervene":false,"reason":"证据不足"}`, false},
		{`{"shouldIntervene":true,"reason":"需确认预算变更","intent":{"type":"clarify","objective":"确认本次预算","evidenceMessageIds":["old","current"]}}`, true},
	} {
		model := &sequenceModel{outputs: []string{tc.output}}
		judge, _ := New(model, "test", 32768)
		result, err := judge.Decide(context.Background(), verification())
		if err != nil || result.Decision.ShouldIntervene != tc.intervene || result.Decision.Breakdowns == nil || len(model.requests) != 1 {
			t.Fatalf("compact verification failed: %+v err=%v", result, err)
		}
	}
}

func TestCompactBranchesCannotBypassEvidenceOrWrites(t *testing.T) {
	for _, output := range []string{
		`{"action":"intervene","reason":"缺少话题和意图"}`,
		`{"action":"verify","reason":"缺少核验问题"}`,
		`{"action":"skip","reason":"偷偷介入","intent":{"type":"answer","objective":"回答","evidenceMessageIds":["current"]}}`,
		`{"action":"intervene","reason":"编造来源","topicContext":{"topic":"请求","continuity":"new_topic","messageIds":["current"],"anchorMessageIds":["current"]},"intent":{"type":"answer","objective":"回答","evidenceMessageIds":["foreign"]}}`,
	} {
		model := &sequenceModel{outputs: []string{output}}
		judge, _ := New(model, "test", 32768)
		if _, err := judge.Discover(context.Background(), window()); !errors.Is(err, ai.ErrInvalidDecision) || len(model.requests) != 1 {
			t.Fatalf("unsafe compact output accepted/repaired: %s err=%v calls=%d", output, err, len(model.requests))
		}
	}
	// The discovery write intent still requires evidence from the current author.
	input := window()
	input.Messages = append([]ai.Message{{ID: "old"}}, input.Messages...)
	output := `{"action":"intervene","reason":"历史操作","topicContext":{"topic":"提醒","continuity":"continuation","messageIds":["old","current"],"anchorMessageIds":["current"]},"intent":{"type":"execute_request","objective":"创建提醒","evidenceMessageIds":["old"]}}`
	model := &sequenceModel{outputs: []string{output}}
	judge, _ := New(model, "test", 32768)
	if _, err := judge.Discover(context.Background(), input); !errors.Is(err, ai.ErrInvalidDecision) || len(model.requests) != 1 {
		t.Fatalf("compact output authorized historical write: %v", err)
	}
}

func TestCompactDecodeClearsOmittedFields(t *testing.T) {
	resolved, _, err := candidateSchema()
	if err != nil {
		t.Fatal(err)
	}
	candidate := ai.InterventionCandidate{TopicContext: &ai.TopicContext{Topic: "stale"}, Intent: &ai.InterventionIntent{Type: "execute_request"}, Verification: &ai.VerificationQuestion{Topic: "stale"}, Breakdowns: []ai.Breakdown{{Type: "conflict"}}}
	if err := decodeDecision(`{"action":"skip","reason":"正常聊天"}`, resolved, &candidate); err != nil {
		t.Fatal(err)
	}
	if candidate.TopicContext != nil || candidate.Intent != nil || candidate.Verification != nil || candidate.Breakdowns == nil || len(candidate.Breakdowns) != 0 {
		t.Fatalf("retained stale fields: %+v", candidate)
	}
}

func TestCompactDiscoveryRepairsMalformedBranchOnce(t *testing.T) {
	model := &sequenceModel{outputs: []string{`{"action":"skip","reason":"闲聊","objective":"错误顶层字段"}`, `{"action":"skip","reason":"闲聊"}`}}
	judge, _ := New(model, "test", 32768)
	result, err := judge.Discover(context.Background(), window())
	if err != nil || result.Decision.Action != "skip" || len(model.requests) != 2 || len(result.Steps) != 2 || result.Steps[0].Status != "failed" {
		t.Fatalf("compact repair failed: %+v calls=%d err=%v", result, len(model.requests), err)
	}
}

func TestCompactVerificationStillRejectsInvalidIntent(t *testing.T) {
	for _, output := range []string{
		`{"shouldIntervene":false,"reason":"偷偷介入","intent":{"type":"clarify","objective":"确认","evidenceMessageIds":["old","current"]}}`,
		`{"shouldIntervene":true,"reason":"遗漏意图"}`,
		`{"shouldIntervene":true,"reason":"缺少历史证据","intent":{"type":"answer","objective":"回答","evidenceMessageIds":["current"]}}`,
	} {
		model := &sequenceModel{outputs: []string{output}}
		judge, _ := New(model, "test", 32768)
		if _, err := judge.Decide(context.Background(), verification()); !errors.Is(err, ai.ErrInvalidDecision) || len(model.requests) != 1 {
			t.Fatalf("invalid compact intent accepted/repaired: %s err=%v calls=%d", output, err, len(model.requests))
		}
	}
}
