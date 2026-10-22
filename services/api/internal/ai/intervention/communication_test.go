package intervention

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
)

type communicationCase struct {
	Name      string       `json:"name"`
	Breakdown string       `json:"breakdown"`
	Messages  []ai.Message `json:"messages"`
}

func communicationCases(t *testing.T) []communicationCase {
	t.Helper()
	data, err := os.ReadFile("testdata/communication.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []communicationCase
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	return cases
}

// Scripted inference verifies the schema and evidence contract, not the model's
// ability to recognize the examples; the opt-in real-model test uses the same cases.
func TestCommunicationDiscoveryContract(t *testing.T) {
	for _, tc := range communicationCases(t) {
		t.Run(tc.Name, func(t *testing.T) {
			ids := []string{}
			for _, message := range tc.Messages {
				ids = append(ids, message.ID)
			}
			current := ids[len(ids)-1]
			decision := ai.InterventionCandidate{Action: "skip", Reason: "成员正在处理或无需重复帮助", Breakdowns: []ai.Breakdown{}}
			if tc.Breakdown != "" {
				decision.TopicContext = &ai.TopicContext{Topic: tc.Name, Continuity: "continuation", MessageIDs: ids, AnchorMessageIDs: []string{current}}
				decision.Action = "intervene"
				decision.Reason = "有明确尚未解决的沟通卡点"
				decision.Breakdowns = []ai.Breakdown{{Type: tc.Breakdown, EvidenceMessageIDs: ids}}
				decision.Intent = &ai.InterventionIntent{Type: "clarify", Objective: "帮助双方明确当前分歧", EvidenceMessageIDs: ids}
			}
			output, err := json.Marshal(decision)
			if err != nil {
				t.Fatal(err)
			}
			model := &sequenceModel{outputs: []string{string(output)}}
			judge, _ := New(model, "test", 32768)
			result, err := judge.Discover(context.Background(), ai.RecognitionInput{TriggerMode: "proactive", CurrentMessageID: current, Messages: tc.Messages})
			if err != nil || result.Decision.Action != decision.Action || len(model.requests) != 1 {
				t.Fatalf("discovery contract failed: result=%+v err=%v", result, err)
			}
		})
	}
}

func TestCommunicationRequiresTwoHumanSpeakers(t *testing.T) {
	for _, kind := range []string{"understanding_mismatch", "support_mismatch", "communication_escalation", "pacing_conflict", "repair_blocked"} {
		for _, scenario := range []string{"one_message", "same_speaker", "assistant_evidence", "unknown_identity", "valid"} {
			t.Run(kind+"/"+scenario, func(t *testing.T) {
				input := ai.RecognitionInput{CurrentMessageID: "b", Messages: []ai.Message{{ID: "a", SenderID: "alice", SenderType: "user"}, {ID: "b", SenderID: "bob", SenderType: "user"}}}
				ids := []string{"a", "b"}
				switch scenario {
				case "one_message":
					ids = []string{"b"}
				case "same_speaker":
					input.Messages[1].SenderID = "alice"
				case "assistant_evidence":
					input.Messages[0].SenderType = "assistant"
				case "unknown_identity":
					input.Messages[0].SenderID = ""
				}
				decision := ai.Recognition{ShouldIntervene: true, Reason: "明确的互动卡点", Breakdowns: []ai.Breakdown{{Type: kind, EvidenceMessageIDs: ids}}, Intent: &ai.InterventionIntent{Type: "clarify", Objective: "澄清当前卡点", EvidenceMessageIDs: ids}}
				err := validateRecognition(decision, input)
				var rejection *candidateRejection
				soft := scenario == "one_message" || scenario == "same_speaker"
				if scenario == "valid" && err != nil || soft && !errors.As(err, &rejection) || !soft && scenario != "valid" && !errors.Is(err, ai.ErrInvalidDecision) {
					t.Fatalf("unexpected validation: %v", err)
				}
			})
		}
	}
}

func TestCommunicationVerificationUsesBothHumanSources(t *testing.T) {
	input := verification()
	input.Conversation.Messages[0].SenderID = "bob"
	input.Conversation.Messages[0].Text = "不行，现在必须继续说清楚，按之前约定不能暂停。"
	input.Evidence.Messages[0].SenderID = "alice"
	input.Evidence.Messages[0].Text = "我情绪上来时需要暂停半小时，我们之前约好了。"
	output := `{"shouldIntervene":true,"reason":"暂停诉求与继续要求冲突","breakdowns":[{"type":"pacing_conflict","evidenceMessageIds":["old","current"]}],"intent":{"type":"suggest","objective":"依据原文确认暂停约定并协商恢复沟通","evidenceMessageIds":["old","current"]}}`
	model := &sequenceModel{outputs: []string{output}}
	judge, _ := New(model, "test", 32768)
	result, err := judge.Decide(context.Background(), input)
	if err != nil || !result.Decision.ShouldIntervene || result.Decision.Breakdowns[0].Type != "pacing_conflict" {
		t.Fatalf("verification lost communication evidence: %+v %v", result, err)
	}
}
