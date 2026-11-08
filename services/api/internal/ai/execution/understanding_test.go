package execution

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	mainagent "github.com/LizHu95/Jetaime/services/api/internal/ai/agent/main"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/intervention"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/respond"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/silence"
)

const cookingMisunderstanding = "做饭提醒澄清后仍被理解为控制饮食"
const returnTimeMisunderstanding = "晚归告知澄清后仍被理解为限制出门"
const householdBurdenConflict = "家务请求与明确工作负担分工冲突"

// Reuse the recognition acceptance inputs so the routing and real generation
// checks exercise the same conversation, rather than a simplified two-line pair.
func understandingInput(t *testing.T, name string) ai.Context {
	t.Helper()
	data, err := os.ReadFile("../intervention/testdata/communication.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name     string       `json:"name"`
		Messages []ai.Message `json:"messages"`
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		if tc.Name != name {
			continue
		}
		at := time.Date(2026, 10, 25, 21, 0, 0, 0, time.FixedZone("Asia/Shanghai", 8*3600))
		input := ai.Context{GroupID: "daily", Users: []ai.User{{ID: "alice", Nickname: "小林"}, {ID: "bob", Nickname: "小周"}}, Messages: tc.Messages}
		for i := range input.Messages {
			message := &input.Messages[i]
			message.GroupID = input.GroupID
			message.SentAt = at.Add(time.Duration(i) * time.Minute)
			message.SenderName = "小林"
			if message.SenderID == "bob" {
				message.SenderName = "小周"
			}
		}
		current := input.Messages[len(input.Messages)-1]
		input.ActorID = current.SenderID
		input.Request = &ai.RequestContext{MessageID: current.ID, SenderID: current.SenderID, SentAt: current.SentAt, CurrentTime: current.SentAt, Timezone: "Asia/Shanghai"}
		return input
	}
	t.Fatalf("missing misunderstanding case %q", name)
	return ai.Context{}
}

// Scripted output checks the evidence handoff and Skill route; real inference
// below separately checks whether the model actually chooses and uses them.
func TestUnderstandingInterventionPreservesOriginalClarificationAndLatestReply(t *testing.T) {
	input := understandingInput(t, cookingMisunderstanding)
	ids := []string{"prep", "vegetables", "clarified", "misunderstood", "control"}
	evidence := []string{"prep", "clarified", "control"}
	candidate := ai.InterventionCandidate{
		TopicContext: &ai.TopicContext{Topic: "今晚做饭准备", Continuity: "continuation", MessageIDs: ids, AnchorMessageIDs: evidence},
		Action:       "intervene", Reason: "已澄清解冻肉，对方后续仍理解为控制饮食",
		Breakdowns: []ai.Breakdown{{Type: "understanding_mismatch", EvidenceMessageIDs: evidence}},
		Intent:     &ai.InterventionIntent{Type: "clarify", Objective: "温和复述解冻肉的提醒，澄清与控制饮食的理解差异", EvidenceMessageIDs: evidence},
	}
	raw, err := json.Marshal(candidate)
	if err != nil {
		t.Fatal(err)
	}
	model := &proactiveModel{outputs: []string{string(raw), `{"name":"respond","arguments":{"status":"answered","answer":"小周，刚才提醒的是做饭前解冻肉，没有提出让你吃蔬菜或替你决定吃什么。这里可能有点误会。","sourceMessageIds":["prep","clarified","control"]}}`}}
	registry, err := tools.New(respond.New(), silence.New())
	if err != nil {
		t.Fatal(err)
	}
	agent, err := mainagent.NewAgent(model, "test", 32768, registry)
	if err != nil {
		t.Fatal(err)
	}
	run, err := invokePassiveEntry(t, agent, input)
	if err != nil || run.Silent || run.Answer == nil || run.Skill == nil || run.Skill.ID != "check-understanding" || run.Skill.Version != "v3" || len(model.requests) != 2 {
		t.Fatalf("misunderstanding did not route to direct clarification: %+v %v", run, err)
	}
	var payload struct {
		Context ai.Context `json:"context"`
	}
	if err := json.Unmarshal([]byte(model.requests[1].Messages[1].Content), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Context.Request.AllowWrites {
		t.Fatal("clarification enabled writes")
	}
	for _, id := range evidence {
		found, pinned := false, false
		for _, message := range payload.Context.Messages {
			found = found || message.ID == id
		}
		for _, required := range payload.Context.Request.RelatedMessageIDs {
			pinned = pinned || required == id
		}
		if !found || !pinned {
			t.Fatalf("lost required original, clarification or latest response: %s", id)
		}
	}
}

func TestHouseholdConflictPreservesBothConcernsWithoutCommunicationSkill(t *testing.T) {
	input := understandingInput(t, householdBurdenConflict)
	ids := []string{"garbage", "burden", "work", "cleaning"}
	candidate := ai.InterventionCandidate{
		TopicContext: &ai.TopicContext{Topic: "当前家务分担", Continuity: "continuation", MessageIDs: ids, AnchorMessageIDs: ids},
		Action:       "intervene", Reason: "异味处理请求与明确的工作负担、分担诉求尚未对齐",
		Breakdowns: []ai.Breakdown{{Type: "conflict", EvidenceMessageIDs: ids}},
		Intent:     &ai.InterventionIntent{Type: "suggest", Objective: "接住异味困扰和工作负担，帮助双方协商本次垃圾处理的分担方式", EvidenceMessageIDs: ids},
	}
	raw, err := json.Marshal(candidate)
	if err != nil {
		t.Fatal(err)
	}
	model := &proactiveModel{outputs: []string{string(raw), `{"name":"respond","arguments":{"status":"answered","answer":"小林在意垃圾异味，小周说工作很忙，也希望家务一起分担。可以一起商量这次怎么安排，才能让双方都承担得过来。","sourceMessageIds":["garbage","burden","work","cleaning"]}}`}}
	registry, err := tools.New(respond.New(), silence.New())
	if err != nil {
		t.Fatal(err)
	}
	agent, err := mainagent.NewAgent(model, "test", 32768, registry)
	if err != nil {
		t.Fatal(err)
	}
	run, err := invokePassiveEntry(t, agent, input)
	if err != nil || run.Silent || run.Answer == nil || run.Skill != nil || len(model.requests) != 2 {
		t.Fatalf("joint conflict was lost or misrouted to emotional support: %+v %v", run, err)
	}
	var payload struct {
		Context ai.Context `json:"context"`
	}
	if err := json.Unmarshal([]byte(model.requests[1].Messages[1].Content), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Context.Request.AllowWrites || len(payload.Context.Messages) != len(input.Messages) {
		t.Fatal("joint negotiation lost evidence or enabled a write")
	}
	for i, message := range payload.Context.Messages {
		original := input.Messages[i]
		if message.ID != original.ID || message.SenderID != original.SenderID || message.SenderType != original.SenderType || message.GroupID != original.GroupID || message.Text != original.Text || !message.SentAt.Equal(original.SentAt) {
			t.Fatal("joint negotiation changed either member's original statement")
		}
	}
}

// Opt-in only: run the real judge, server routing and main Agent with only
// response/silence tools. This cannot send a chat message or perform a write.
func TestRealModelUnderstandingIntervention(t *testing.T) {
	entry, config := realCommunicationEntry(t)
	for _, name := range []string{cookingMisunderstanding, returnTimeMisunderstanding} {
		t.Run(name, func(t *testing.T) {
			input := understandingInput(t, name)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			run, err := entry.Invoke(ctx, ai.Invocation{Context: input, TriggerMode: "proactive"})
			if err != nil || run.Silent || run.Answer == nil || run.Skill == nil || run.Skill.ID != "check-understanding" {
				t.Fatalf("expected unsolicited clarification, got %+v %v", run, err)
			}
			answer := run.Answer.Answer
			t.Logf("model=%s skill=%s version=%s answer=%s", config.Model, run.Skill.ID, run.Skill.Version, answer)
			if strings.ContainsAny(answer, "?？") {
				t.Fatal("clear original was replaced with another question")
			}
			if len(run.Answer.SourceMessageIDs) == 0 {
				t.Fatal("clarification omitted conversation sources")
			}
			for _, unsupported := range []string{"你理解错了", "别想太多", "绝不想控制", "只是关心你", "为了你好", "需要我帮忙吗", "clarified"} {
				if strings.Contains(answer, unsupported) {
					t.Fatalf("judgment, invented motive or internal identifier: %s", unsupported)
				}
			}
		})
	}
}

func realCommunicationEntry(t *testing.T) (*entry, llm.Config) {
	t.Helper()
	path := os.Getenv("JETAIME_PROACTIVE_MODEL_CONFIG")
	if path == "" {
		t.Skip("set JETAIME_PROACTIVE_MODEL_CONFIG for real clarification acceptance")
	}
	flags := llm.RegisterFlags(flag.NewFlagSet("understanding-acceptance", flag.ContinueOnError))
	flags.File, flags.Profile = path, os.Getenv("JETAIME_PROACTIVE_MODEL_PROFILE")
	config, err := flags.Resolve(32768)
	if err != nil {
		t.Fatal(err)
	}
	model, err := config.New(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	judge, err := intervention.New(model, config.Model, config.ContextTokens)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := tools.New(respond.New(), silence.New())
	if err != nil {
		t.Fatal(err)
	}
	agent, err := mainagent.NewAgent(model, config.Model, config.ContextTokens, registry)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := newEntry(agent, judge)
	if err != nil {
		t.Fatal(err)
	}
	return entry, config
}

func TestRealModelBalancedHouseholdIntervention(t *testing.T) {
	entry, config := realCommunicationEntry(t)
	for _, reversed := range []bool{false, true} {
		name := "original_roles"
		if reversed {
			name = "reversed_roles"
		}
		t.Run(name, func(t *testing.T) {
			input := understandingInput(t, householdBurdenConflict)
			if reversed {
				for i := range input.Messages {
					message := &input.Messages[i]
					if message.SenderID == "alice" {
						message.SenderID, message.SenderName = "bob", "小周"
					} else {
						message.SenderID, message.SenderName = "alice", "小林"
					}
				}
				input.ActorID = input.Messages[len(input.Messages)-1].SenderID
				input.Request.SenderID = input.ActorID
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			run, err := entry.Invoke(ctx, ai.Invocation{Context: input, TriggerMode: "proactive"})
			if err != nil || run.Silent || run.Answer == nil || run.Skill != nil || run.Recognition == nil || len(run.Recognition.Breakdowns) == 0 || run.Recognition.Breakdowns[0].Type != "conflict" {
				t.Fatalf("expected joint conflict assistance without a support-mismatch Skill: %+v %v", run, err)
			}
			answer := run.Answer.Answer
			t.Logf("model=%s answer=%s", config.Model, answer)
			// Accept equivalent descriptions of the immediate household concern;
			// the check is about both concerns, not a fixed response template.
			household := strings.Contains(answer, "垃圾") || strings.Contains(answer, "异味") || strings.Contains(answer, "卫生")
			if !strings.Contains(answer, "小林") || !strings.Contains(answer, "小周") || !household || !strings.Contains(answer, "工作") {
				t.Fatal("joint response did not name both expressed concerns")
			}
			for _, unsupported := range []string{"先去倒", "先把垃圾倒", "半小时后", "非防御", "分工确实不公平", "都是你", "需要我帮忙吗"} {
				if strings.Contains(answer, unsupported) {
					t.Fatalf("unilateral obligation or unsupported judgment: %s", unsupported)
				}
			}
		})
	}
}
