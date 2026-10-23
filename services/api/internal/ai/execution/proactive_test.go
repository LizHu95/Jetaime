package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	mainagent "github.com/LizHu95/Jetaime/services/api/internal/ai/agent/main"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/intervention"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/respond"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/silence"
	"github.com/google/jsonschema-go/jsonschema"
	"time"
)

type proactiveModel struct {
	outputs  []string
	requests []llm.Request
}

func TestProactiveRepairsMisnestedObjectiveBeforeExecutingTool(t *testing.T) {
	// Replay the production failure: intent was correct, but objective was at
	// the root, rather than in intent. Do not silently accept the invalid shape.
	malformed := `{"topicContext":{"topic":"当前测试事项","continuity":"continuation","messageIds":["m1","m6"],"anchorMessageIds":["m6"]},"action":"intervene","verification":null,"intent":{"type":"execute_request","evidenceMessageIds":["m6","m1"]},"objective":"根据用户提供的体检时间和地点创建提醒","reason":"用户明确请求创建提醒","breakdowns":[]}`
	corrected := `{"topicContext":{"topic":"当前测试事项","continuity":"continuation","messageIds":["m1","m6"],"anchorMessageIds":["m6"]},"action":"intervene","verification":null,"intent":{"type":"execute_request","objective":"根据用户提供的体检时间和地点创建提醒","evidenceMessageIds":["m6","m1"]},"reason":"用户明确请求创建提醒","breakdowns":[]}`
	model := &proactiveModel{outputs: []string{malformed, corrected, `{"name":"save_item","arguments":{"mode":"save"}}`}}
	writes := 0
	save := tools.NewTyped(tools.Metadata{Name: "save_item", Description: "保存", Terminal: true}, func(context.Context, tools.Scope, saveInput) (saveOutput, error) {
		writes++
		return saveOutput{Status: "saved"}, nil
	}, saveContractOptions())
	a, _ := mainagent.NewAgent(model, "test", 32768)
	a.Tools, _ = tools.New(save)
	input := testContext()
	input.Request = &ai.RequestContext{MessageID: "m6", SenderID: input.ActorID, SentAt: input.Messages[1].SentAt}
	run, err := invokePassiveEntry(t, a, input)
	if err != nil || writes != 1 || len(model.requests) != 3 || run.Recognition == nil || run.Recognition.Intent.Objective == "" {
		t.Fatalf("repair did not execute exactly once: writes=%d err=%v run=%+v", writes, err, run)
	}
	if run.Steps[0].Status != "failed" || run.Steps[1].Status != "succeeded" || !strings.Contains(run.Steps[0].Error, "objective") {
		t.Fatal("missing structural validation diagnostics", run.Steps)
	}
	var schemaFromPrompt map[string]any
	prefix := "输出JSON Schema（必须严格遵守）："
	_, schema, ok := strings.Cut(model.requests[0].Messages[0].Content, prefix)
	if !ok || json.Unmarshal([]byte(schema), &schemaFromPrompt) != nil {
		t.Fatal("JSON-object provider did not receive the schema")
	}
	if len(model.requests[1].Messages) != 4 || !strings.Contains(model.requests[1].Messages[3].Content, "intent") {
		t.Fatal("repair did not receive validation feedback")
	}
}

func TestProactiveStopsAfterOneFormatRepair(t *testing.T) {
	malformed := `{"topicContext":{"topic":"当前测试事项","continuity":"continuation","messageIds":["m1","m6"],"anchorMessageIds":["m6"]},"action":"intervene","verification":null,"reason":"请求","breakdowns":[],"intent":{"type":"execute_request","evidenceMessageIds":["m6"]},"objective":"创建提醒"}`
	model := &proactiveModel{outputs: []string{malformed, malformed}}
	a, _ := mainagent.NewAgent(model, "test", 32768)
	input := testContext()
	input.Request = &ai.RequestContext{MessageID: "m6", SenderID: input.ActorID, SentAt: input.Messages[1].SentAt}
	run, err := invokePassiveEntry(t, a, input)
	if !errors.Is(err, ai.ErrInvalidDecision) || len(model.requests) != 2 || run.Recognition != nil {
		t.Fatalf("invalid output reached execution: calls=%d err=%v", len(model.requests), err)
	}
}

func (m *proactiveModel) Call(_ context.Context, req llm.Request) (llm.Result, error) {
	m.requests = append(m.requests, req)
	if len(m.outputs) == 0 {
		return llm.Result{}, errors.New("unexpected model call")
	}
	out := m.outputs[0]
	m.outputs = m.outputs[1:]
	return llm.Result{Content: out}, nil
}

func TestProactiveRecognitionAndExecution(t *testing.T) {
	for _, tc := range []struct {
		name, decision, next string
		silent, invalid      bool
	}{
		{"silence", `{"topicContext":{"topic":"当前测试事项","continuity":"continuation","messageIds":["m1","m6"],"anchorMessageIds":["m6"]},"action":"skip","verification":null,"reason":"正在推进","breakdowns":[],"intent":null}`, "", true, false},
		{"respond", `{"topicContext":{"topic":"当前测试事项","continuity":"continuation","messageIds":["m1","m6"],"anchorMessageIds":["m6"]},"action":"intervene","verification":null,"reason":"需要澄清","breakdowns":[{"type":"uncertainty","evidenceMessageIds":["m6"]}],"intent":{"type":"clarify","objective":"确认测试范围","evidenceMessageIds":["m6"]}}`, `{"name":"respond","arguments":{"status":"answered","answer":"先确认测试范围可以吗？","sourceMessageIds":["m6"]}}`, false, false},
		{"abstain after recognition", `{"topicContext":{"topic":"当前测试事项","continuity":"continuation","messageIds":["m1","m6"],"anchorMessageIds":["m6"]},"action":"intervene","verification":null,"reason":"可能需要澄清","breakdowns":[],"intent":{"type":"clarify","objective":"核实是否需要帮助","evidenceMessageIds":["m6"]}}`, `{"name":"remain_silent","arguments":{"reason":"无需补充"}}`, true, false},
		{"foreign evidence", `{"topicContext":{"topic":"当前测试事项","continuity":"continuation","messageIds":["m1","m6"],"anchorMessageIds":["m6"]},"action":"intervene","verification":null,"reason":"需要澄清","breakdowns":[],"intent":{"type":"clarify","objective":"询问","evidenceMessageIds":["foreign"]}}`, "", false, true},
		{"missing decision", `{"reason":"漏字段","breakdowns":[],"intent":null}`, "", false, true},
		{"unknown breakdown", `{"topicContext":{"topic":"当前测试事项","continuity":"continuation","messageIds":["m1","m6"],"anchorMessageIds":["m6"]},"action":"skip","verification":null,"reason":"未知类别","breakdowns":[{"type":"other","evidenceMessageIds":["m6"]}],"intent":null}`, "", false, true},
		{"null breakdowns", `{"topicContext":{"topic":"当前测试事项","continuity":"continuation","messageIds":["m1","m6"],"anchorMessageIds":["m6"]},"action":"skip","verification":null,"reason":"错误结构","breakdowns":null,"intent":null}`, "", false, true},
		{"write without current evidence", `{"topicContext":{"topic":"当前测试事项","continuity":"continuation","messageIds":["m1","m6"],"anchorMessageIds":["m6"]},"action":"intervene","verification":null,"reason":"操作","breakdowns":[],"intent":{"type":"execute_request","objective":"设置提醒","evidenceMessageIds":["m1"]}}`, "", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := &proactiveModel{outputs: []string{tc.decision}}
			if tc.invalid {
				model.outputs = append(model.outputs, tc.decision)
			}
			if tc.next != "" {
				model.outputs = append(model.outputs, tc.next)
			}
			a, _ := mainagent.NewAgent(model, "test", 16384)
			a.Tools, _ = tools.New(respond.New(), silence.New())
			input := testContext()
			input.Request = &ai.RequestContext{MessageID: "m6", SenderID: input.ActorID, SentAt: input.Messages[1].SentAt}
			run, err := invokePassiveEntry(t, a, input)
			if tc.invalid {
				if !errors.Is(err, ai.ErrInvalidDecision) {
					t.Fatalf("expected invalid decision, got %v", err)
				}
				return
			}
			if err != nil || run.Silent != tc.silent || run.Recognition == nil {
				t.Fatalf("run=%+v err=%v", run, err)
			}
			var payload ai.RecognitionInput
			if err = json.Unmarshal([]byte(model.requests[0].Messages[1].Content), &payload); err != nil || payload.CurrentMessageID != "m6" || len(payload.Messages) != 2 {
				t.Fatal("invalid recognition input", err)
			}
			if tc.next == "" && len(model.requests) != 1 {
				t.Fatal("silence executed main agent")
			}
		})
	}
}

func TestProactiveCanRemainSilentAfterReadTool(t *testing.T) {
	model := &proactiveModel{outputs: []string{
		`{"topicContext":{"topic":"当前测试事项","continuity":"continuation","messageIds":["m1","m6"],"anchorMessageIds":["m6"]},"action":"intervene","verification":null,"reason":"需要查证","breakdowns":[],"intent":{"type":"remind","objective":"核实约束是否仍有效","evidenceMessageIds":["m6"]}}`,
		`{"name":"lookup","arguments":{}}`,
		`{"name":"remain_silent","arguments":{"reason":"已有人处理，无需重复"}}`,
	}}
	reads := 0
	lookup := tools.NewTyped(tools.Metadata{Name: "lookup", Description: "查证", ReadOnly: true, Terminal: true}, func(context.Context, tools.Scope, struct{}) (struct{}, error) { reads++; return struct{}{}, nil })
	a, _ := mainagent.NewAgent(model, "test", 16384)
	a.Tools, _ = tools.New(lookup, respond.New(), silence.New())
	input := testContext()
	input.Request = &ai.RequestContext{MessageID: "m6", SenderID: input.ActorID, SentAt: input.Messages[1].SentAt}
	run, err := invokePassiveEntry(t, a, input)
	if err != nil || !run.Silent || reads != 1 || len(model.requests) != 3 {
		t.Fatalf("read must allow abstention: run=%+v reads=%d err=%v", run, reads, err)
	}
}

func TestProactiveWriteRequiresExplicitRequestIntent(t *testing.T) {
	for _, intent := range []string{"remind", "execute_request"} {
		t.Run(intent, func(t *testing.T) {
			decision := `{"topicContext":{"topic":"当前测试事项","continuity":"continuation","messageIds":["m1","m6"],"anchorMessageIds":["m6"]},"action":"intervene","verification":null,"reason":"需要帮助","breakdowns":[],"intent":{"type":"` + intent + `","objective":"处理提醒","evidenceMessageIds":["m6"]}}`
			model := &proactiveModel{outputs: []string{decision, `{"name":"save_item","arguments":{"mode":"save"}}`, `{"name":"remain_silent","arguments":{"reason":"没有操作授权"}}`}}
			writes := 0
			save := tools.NewTyped(tools.Metadata{Name: "save_item", Description: "保存", Terminal: true}, func(context.Context, tools.Scope, saveInput) (saveOutput, error) {
				writes++
				return saveOutput{Status: "saved"}, nil
			}, saveContractOptions())
			a, _ := mainagent.NewAgent(model, "test", 16384)
			a.Tools, _ = tools.New(save, silence.New(), respond.New())
			input := testContext()
			input.Request = &ai.RequestContext{MessageID: "m6", SenderID: input.ActorID, SentAt: input.Messages[1].SentAt}
			run, err := invokePassiveEntry(t, a, input)
			if err != nil {
				t.Fatal(err)
			}
			if intent == "remind" && (writes != 0 || !run.Silent) {
				t.Fatal("obstacle authorized a write")
			}
			if intent == "execute_request" && (writes != 1 || run.Answer == nil) {
				t.Fatal("explicit request did not execute")
			}
		})
	}
}

// These integration tests compose the independent entry, judge, and main agent.
func invokePassiveEntry(t *testing.T, main *mainagent.Agent, input ai.Context) (ai.Run, error) {
	t.Helper()
	judge, err := intervention.New(main.ChatModel, main.DefaultModel, main.Tokens)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := newEntry(main, judge)
	if err != nil {
		t.Fatal(err)
	}
	return entry.Invoke(context.Background(), ai.Invocation{Context: input, TriggerMode: "proactive"})
}

func TestCommunicationInterventionDirectResponse(t *testing.T) {
	for _, tc := range []struct{ kind, action, previous, current, answer string }{
		{"understanding_mismatch", "check_understanding", "我在意的是你答应之前没有和我商量，不是不让你去。", "说到底你就是不让我去，你说几次都一样。", "刚才说的是希望提前商量，你理解的重点也是这个吗？"},
		{"support_mismatch", "clarify_support", "我想让你先听我说，不想现在听建议。", "你就应该按我说的方法改，不然有什么好说的。", "她已经说了现在想先被听一听，可以先听完再一起想办法。"},
		{"communication_escalation", "focus_on_event", "这次迟到我等了很久，心里很难受。", "你就是个自私又讨厌的人。", "先聚焦这次迟到：你希望下次对方具体怎么做？"},
		{"pacing_conflict", "negotiate_pause", "我需要暂停一下，缓过来再聊。", "不行，现在必须继续说清楚。", "先尊重暂停，等缓下来后再商量一个双方能接受的时间继续。"},
		{"repair_blocked", "clarify_unresolved_need", "我已经道歉了，就翻篇吧。", "我在意的还有你没提前通知，你还没回应下次怎么办。", "还有下次怎样提前通知没有回应，可以先把这个安排说具体。"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			candidate := ai.InterventionCandidate{
				TopicContext: &ai.TopicContext{Topic: "当前沟通分歧", Continuity: "continuation", MessageIDs: []string{"m1", "m6"}, AnchorMessageIDs: []string{"m1", "m6"}},
				Action:       "intervene", Reason: "明确的互动卡点仍未解决", Breakdowns: []ai.Breakdown{{Type: tc.kind, EvidenceMessageIDs: []string{"m1", "m6"}}},
				Intent: &ai.InterventionIntent{Type: "clarify", Objective: "帮助双方推进当前沟通", EvidenceMessageIDs: []string{"m1", "m6"}},
			}
			decision, err := json.Marshal(candidate)
			if err != nil {
				t.Fatal(err)
			}
			reply, err := json.Marshal(map[string]any{"name": "respond", "arguments": map[string]any{"status": "answered", "answer": tc.answer, "sourceMessageIds": []string{"m1", "m6"}}})
			if err != nil {
				t.Fatal(err)
			}
			model := &proactiveModel{outputs: []string{string(decision), string(reply)}}
			agent, _ := mainagent.NewAgent(model, "test", 32768)
			agent.Tools, _ = tools.New(respond.New(), silence.New())
			input := testContext()
			input.Messages[0].Text = tc.previous
			input.Messages[1].Text = tc.current
			input.Messages[1].SenderID = "xia"
			input.ActorID = "xia"
			input.Request = &ai.RequestContext{MessageID: "m6", SenderID: "xia", SentAt: input.Messages[1].SentAt}
			run, err := invokePassiveEntry(t, agent, input)
			if err != nil || run.Silent || run.Recognition == nil || len(model.requests) != 2 {
				t.Fatalf("direct intervention failed: run=%+v err=%v calls=%d", run, err, len(model.requests))
			}
			var payload struct {
				Context  ai.Context `json:"context"`
				Question string     `json:"question"`
			}
			if err := json.Unmarshal([]byte(model.requests[1].Messages[1].Content), &payload); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(payload.Question, tc.action) || payload.Context.Request.AllowWrites || len(payload.Context.Request.RelatedMessageIDs) != 2 {
				t.Fatalf("lost help action, evidence, or write boundary: %+v", payload)
			}
		})
	}
}

func TestRejectedCommunicationCandidateEndsSuccessfullyWithoutMain(t *testing.T) {
	// Reproduce the production case: two statements by the same person were
	// mistaken for support mismatch before their partner had responded.
	candidate := ai.InterventionCandidate{
		TopicContext: &ai.TopicContext{Topic: "手办损坏", Continuity: "new_topic", MessageIDs: []string{"m1", "m6"}, AnchorMessageIDs: []string{"m1", "m6"}},
		Action:       "intervene", Reason: "表达难过需要沟通帮助", Breakdowns: []ai.Breakdown{{Type: "support_mismatch", EvidenceMessageIDs: []string{"m1", "m6"}}},
		Intent: &ai.InterventionIntent{Type: "suggest", Objective: "安抚情绪并建议沟通补救", EvidenceMessageIDs: []string{"m1", "m6"}},
	}
	raw, _ := json.Marshal(candidate)
	model := &proactiveModel{outputs: []string{string(raw)}}
	agent, _ := mainagent.NewAgent(model, "test", 32768)
	agent.Tools, _ = tools.New(respond.New(), silence.New())
	input := testContext()
	input.Messages[0].Text = "你把我的手办弄坏了"
	input.Messages[1].Text = "我很难过"
	input.Request = &ai.RequestContext{MessageID: "m6", SenderID: input.ActorID, SentAt: input.Messages[1].SentAt}
	run, err := invokePassiveEntry(t, agent, input)
	if err != nil || !run.Silent || run.Status != "succeeded" || run.Error != "" || len(model.requests) != 1 || run.Recognition == nil || run.Recognition.ShouldIntervene || run.Recognition.Intent != nil {
		t.Fatalf("normal evidence rejection became a failed workflow: %+v %v", run, err)
	}
	if len(run.Steps) != 1 || run.Steps[0].Status != "succeeded" || run.Steps[0].Error != "" {
		t.Fatalf("rejected candidate retained error steps: %+v", run.Steps)
	}
	if diagnostic := run.Steps[0].Output.(map[string]any); diagnostic["outcome"] != "candidate_rejected" || diagnostic["reasonCode"] != "insufficient_participant_evidence" {
		t.Fatalf("missing quality diagnostic: %+v", diagnostic)
	}
}

func testContext() ai.Context {
	at := time.Date(2026, 10, 10, 10, 0, 0, 0, time.FixedZone("Asia/Shanghai", 8*3600))
	return ai.Context{GroupID: "daily", ActorID: "lin", Users: []ai.User{{ID: "lin", Nickname: "测试用户 A"}, {ID: "xia", Nickname: "测试用户 B"}}, Tasks: []ai.Task{}, Messages: []ai.Message{
		{ID: "m1", GroupID: "daily", SenderID: "lin", SentAt: at, Text: "测试消息"},
		{ID: "m6", GroupID: "daily", SenderID: "lin", SentAt: at.Add(time.Minute), Text: "我明天负责测试任务"},
	}}
}

type saveInput struct {
	Mode string `json:"mode"`
}
type saveOutput struct {
	Status string `json:"status"`
}

func saveContractOptions() tools.Options[saveOutput] {
	return tools.Options[saveOutput]{Input: func(s *jsonschema.Schema) { s.Properties["mode"].Enum = []any{"save"} }, Output: func(s *jsonschema.Schema) { s.Properties["status"].Enum = []any{"saved"} }, Present: func(saveOutput) tools.Completion {
		return tools.Completion{Answer: &ai.Answer{Status: "answered", Answer: "已保存", SourceMessageIDs: []string{}}}
	}}
}

func thirtyMessageContext() ai.Context {
	input := testContext()
	seed := input.Messages[0]
	input.Messages = nil
	for i := 0; i < 30; i++ {
		message := seed
		message.ID = fmt.Sprintf("m%d", i)
		message.SentAt = seed.SentAt.Add(time.Duration(i) * time.Second)
		message.Text = strings.Repeat("background", 20)
		input.Messages = append(input.Messages, message)
	}
	input.Request = &ai.RequestContext{MessageID: "m29", SenderID: input.ActorID, SentAt: input.Messages[29].SentAt, CurrentTime: time.Now(), Timezone: "Asia/Shanghai", TriggerMode: "proactive"}
	return input
}

func TestProactiveRecognizesThirtyButExecutesWithEvidenceAndSixRecent(t *testing.T) {
	model := &proactiveModel{outputs: []string{
		`{"topicContext":{"topic":"当前测试事项","continuity":"continuation","messageIds":["m0","m1","m2","m3","m4","m5","m6","m7","m8","m9","m10","m11","m12","m13","m14","m15","m16","m17","m18","m19","m20","m21","m22","m23","m24","m25","m26","m27","m28","m29"],"anchorMessageIds":["m29"]},"action":"intervene","verification":null,"reason":"需要补充地点","breakdowns":[{"type":"underspecified_plan","evidenceMessageIds":["m2"]}],"intent":{"type":"clarify","objective":"确认地点","evidenceMessageIds":["m0"]}}`,
		`{"name":"remain_silent","arguments":{"reason":"先等待成员补充"}}`,
	}}
	agent, err := mainagent.NewAgent(model, "test", 32768)
	if err != nil {
		t.Fatal(err)
	}
	agent.Tools, err = tools.New(silence.New())
	if err != nil {
		t.Fatal(err)
	}
	input := thirtyMessageContext()
	run, err := invokePassiveEntry(t, agent, input)
	if err != nil || !run.Silent || len(model.requests) != 2 {
		t.Fatal("proactive execution failed", err)
	}
	var recognition ai.RecognitionInput
	if err = json.Unmarshal([]byte(model.requests[0].Messages[1].Content), &recognition); err != nil || len(recognition.Messages) != 30 {
		t.Fatal("recognition lost its thirty-message window", err)
	}
	var execution struct {
		Context ai.Context `json:"context"`
	}
	if err = json.Unmarshal([]byte(model.requests[1].Messages[1].Content), &execution); err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, message := range execution.Context.Messages {
		ids[message.ID] = true
	}
	if len(ids) != 9 || !ids["m0"] || !ids["m2"] || !ids["m29"] || ids["m22"] || !ids["m23"] {
		t.Fatal("execution did not retain exactly its evidence and six recent originals")
	}
	if len(input.Messages) != 30 || len(input.Request.RelatedMessageIDs) != 0 {
		t.Fatal("recognition snapshot mutated")
	}
}
