package mainagent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/respond"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/silence"
)

type proactiveModel struct {
	outputs  []string
	requests []llm.Request
}

func TestProactiveRepairsMisnestedObjectiveBeforeExecutingTool(t *testing.T) {
	// Replay the production failure: intent was correct, but objective was at
	// the root, rather than in intent. Do not silently accept the invalid shape.
	malformed := `{"shouldIntervene":true,"intent":{"type":"execute_request","evidenceMessageIds":["m6","m1"]},"objective":"根据用户提供的体检时间和地点创建提醒","reason":"用户明确请求创建提醒","breakdowns":[]}`
	corrected := `{"shouldIntervene":true,"intent":{"type":"execute_request","objective":"根据用户提供的体检时间和地点创建提醒","evidenceMessageIds":["m6","m1"]},"reason":"用户明确请求创建提醒","breakdowns":[]}`
	model := &proactiveModel{outputs: []string{malformed, corrected, `{"name":"save_item","arguments":{"mode":"save"}}`}}
	writes := 0
	save := tools.NewTyped(tools.Metadata{Name: "save_item", Description: "保存", Terminal: true}, func(context.Context, tools.Scope, saveInput) (saveOutput, error) {
		writes++
		return saveOutput{Status: "saved"}, nil
	}, saveContractOptions())
	a, _ := NewAgent(model, "test", 32768)
	a.Tools, _ = tools.New(save)
	input := testContext()
	input.Request = &ai.RequestContext{MessageID: "m6", SenderID: input.ActorID, SentAt: input.Messages[1].SentAt}
	run, err := a.Invoke(context.Background(), ai.Invocation{Context: input, TriggerMode: "proactive"})
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
	malformed := `{"shouldIntervene":true,"reason":"请求","breakdowns":[],"intent":{"type":"execute_request","evidenceMessageIds":["m6"]},"objective":"创建提醒"}`
	model := &proactiveModel{outputs: []string{malformed, malformed}}
	a, _ := NewAgent(model, "test", 32768)
	input := testContext()
	input.Request = &ai.RequestContext{MessageID: "m6", SenderID: input.ActorID, SentAt: input.Messages[1].SentAt}
	run, err := a.Invoke(context.Background(), ai.Invocation{Context: input, TriggerMode: "proactive"})
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
		{"silence", `{"shouldIntervene":false,"reason":"正在推进","breakdowns":[],"intent":null}`, "", true, false},
		{"respond", `{"shouldIntervene":true,"reason":"需要澄清","breakdowns":[{"type":"uncertainty","evidenceMessageIds":["m6"]}],"intent":{"type":"clarify","objective":"确认测试范围","evidenceMessageIds":["m6"]}}`, `{"name":"respond","arguments":{"status":"answered","answer":"先确认测试范围可以吗？","sourceMessageIds":["m6"]}}`, false, false},
		{"abstain after recognition", `{"shouldIntervene":true,"reason":"可能需要澄清","breakdowns":[],"intent":{"type":"clarify","objective":"核实是否需要帮助","evidenceMessageIds":["m6"]}}`, `{"name":"remain_silent","arguments":{"reason":"无需补充"}}`, true, false},
		{"foreign evidence", `{"shouldIntervene":true,"reason":"需要澄清","breakdowns":[],"intent":{"type":"clarify","objective":"询问","evidenceMessageIds":["foreign"]}}`, "", false, true},
		{"missing decision", `{"reason":"漏字段","breakdowns":[],"intent":null}`, "", false, true},
		{"unknown breakdown", `{"shouldIntervene":false,"reason":"未知类别","breakdowns":[{"type":"other","evidenceMessageIds":["m6"]}],"intent":null}`, "", false, true},
		{"null breakdowns", `{"shouldIntervene":false,"reason":"错误结构","breakdowns":null,"intent":null}`, "", false, true},
		{"write without current evidence", `{"shouldIntervene":true,"reason":"操作","breakdowns":[],"intent":{"type":"execute_request","objective":"设置提醒","evidenceMessageIds":["m1"]}}`, "", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := &proactiveModel{outputs: []string{tc.decision}}
			if tc.invalid {
				model.outputs = append(model.outputs, tc.decision)
			}
			if tc.next != "" {
				model.outputs = append(model.outputs, tc.next)
			}
			a, _ := NewAgent(model, "test", 16384)
			a.Tools, _ = tools.New(respond.New(), silence.New())
			input := testContext()
			input.Request = &ai.RequestContext{MessageID: "m6", SenderID: input.ActorID, SentAt: input.Messages[1].SentAt}
			run, err := a.Invoke(context.Background(), ai.Invocation{Context: input, TriggerMode: "proactive"})
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
		`{"shouldIntervene":true,"reason":"需要查证","breakdowns":[],"intent":{"type":"remind","objective":"核实约束是否仍有效","evidenceMessageIds":["m6"]}}`,
		`{"name":"lookup","arguments":{}}`,
		`{"name":"remain_silent","arguments":{"reason":"已有人处理，无需重复"}}`,
	}}
	reads := 0
	lookup := tools.NewTyped(tools.Metadata{Name: "lookup", Description: "查证", ReadOnly: true, Terminal: true}, func(context.Context, tools.Scope, struct{}) (struct{}, error) { reads++; return struct{}{}, nil })
	a, _ := NewAgent(model, "test", 16384)
	a.Tools, _ = tools.New(lookup, respond.New(), silence.New())
	input := testContext()
	input.Request = &ai.RequestContext{MessageID: "m6", SenderID: input.ActorID, SentAt: input.Messages[1].SentAt}
	run, err := a.Invoke(context.Background(), ai.Invocation{Context: input, TriggerMode: "proactive"})
	if err != nil || !run.Silent || reads != 1 || len(model.requests) != 3 {
		t.Fatalf("read must allow abstention: run=%+v reads=%d err=%v", run, reads, err)
	}
}

func TestProactiveWriteRequiresExplicitRequestIntent(t *testing.T) {
	for _, intent := range []string{"remind", "execute_request"} {
		t.Run(intent, func(t *testing.T) {
			decision := `{"shouldIntervene":true,"reason":"需要帮助","breakdowns":[],"intent":{"type":"` + intent + `","objective":"处理提醒","evidenceMessageIds":["m6"]}}`
			model := &proactiveModel{outputs: []string{decision, `{"name":"save_item","arguments":{"mode":"save"}}`, `{"name":"remain_silent","arguments":{"reason":"没有操作授权"}}`}}
			writes := 0
			save := tools.NewTyped(tools.Metadata{Name: "save_item", Description: "保存", Terminal: true}, func(context.Context, tools.Scope, saveInput) (saveOutput, error) {
				writes++
				return saveOutput{Status: "saved"}, nil
			}, saveContractOptions())
			a, _ := NewAgent(model, "test", 16384)
			a.Tools, _ = tools.New(save, silence.New(), respond.New())
			input := testContext()
			input.Request = &ai.RequestContext{MessageID: "m6", SenderID: input.ActorID, SentAt: input.Messages[1].SentAt}
			run, err := a.Invoke(context.Background(), ai.Invocation{Context: input, TriggerMode: "proactive"})
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
