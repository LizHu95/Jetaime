package mainagent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/agent/main/prompts"
	aicontext "github.com/LizHu95/Jetaime/services/api/internal/ai/context"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
)

type mainTestModel struct {
	decision, answer string
	requests         []llm.Request
}

func (m *mainTestModel) Call(_ context.Context, r llm.Request) (llm.Result, error) {
	m.requests = append(m.requests, r)
	if strings.HasPrefix(r.Messages[0].Content, prompts.Main) {
		return llm.Result{Content: m.decision}, nil
	}
	if m.answer != "" {
		return llm.Result{Content: m.answer}, nil
	}
	return llm.Result{Content: `{"suggestions":[]}`}, nil
}

func TestMainCanDelegateToQuestionTool(t *testing.T) {
	for _, basis := range []string{"general", "history"} {
		t.Run(basis, func(t *testing.T) {
			input := testContext()
			ids := `[]`
			if basis == "history" {
				ids = `["m1"]`
			}
			model := &mainTestModel{decision: `{"name":"answer_question","arguments":{"question":"请帮忙"}}`, answer: `{"status":"answered","answer":"工具回答","sourceMessageIds":` + ids + `}`}
			agent, _ := newTestAgent(model, "test", 16384)
			run, err := agent.ExecuteMain(context.Background(), input, "请帮忙", func(context.Context) (ai.Context, error) { t.Fatal("QA loaded extraction snapshot"); return input, nil })
			if err != nil || run.Answer == nil || run.Answer.Answer != "工具回答" || len(model.requests) != 2 || run.Route.Version != PromptVersion || run.Basis != basis {
				t.Fatalf("QA not delegated: %v calls=%d", err, len(model.requests))
			}
			if !strings.Contains(model.requests[0].Messages[1].Content, "answer_question") {
				t.Fatal("question tool missing from catalog")
			}
		})
	}
}
func TestMainRejectsDisabledExtractionTool(t *testing.T) {
	input := testContext()
	model := &mainTestModel{decision: `{"name":"extract_todos","arguments":{"question":"整理今天的待办"}}`}
	agent, _ := newTestAgent(model, "test", 16384)
	_, err := agent.ExecuteMain(context.Background(), input, "整理今天的待办", func(context.Context) (ai.Context, error) {
		t.Fatal("disabled tool loaded an extraction snapshot")
		return input, nil
	})
	if !errors.Is(err, ai.ErrInvalidDecision) || len(model.requests) != MaxToolSteps+1 {
		t.Fatal("disabled extraction tool executed", err)
	}
}
func TestMainRejectsUnregisteredCallsAndFormerDirectAnswerProtocol(t *testing.T) {
	for _, decision := range []string{`not json`, `{"action":"answer","basis":"general","answer":{"status":"answered","answer":"bypass","sourceMessageIds":[]}}`, `{"name":"delete","arguments":{}}`, `{"name":"answer_question","arguments":null}`, `{"name":"answer_question","arguments":{"question":"test","groupId":"foreign"}}`, `{"name":"answer_question","arguments":{"question":"test"},"extra":true}`} {
		input := testContext()
		model := &mainTestModel{decision: decision}
		agent, _ := newTestAgent(model, "test", 16384)
		_, err := agent.ExecuteMain(context.Background(), input, "test", nil)
		if !errors.Is(err, ai.ErrInvalidDecision) || len(model.requests) != MaxToolSteps+1 {
			t.Fatal("invalid tool call accepted", err)
		}
	}
}
func TestMainContextAndToolCatalogCannotBypassBudget(t *testing.T) {
	input := testContext()
	seed := input.Messages[0]
	for i := 0; i < 30; i++ {
		m := seed
		m.ID = string(rune('A' + i))
		input.Messages = append(input.Messages, m)
	}
	model := &mainTestModel{decision: `{"name":"answer_question","arguments":{"question":"怎么除味？"}}`, answer: `{"status":"answered","answer":"回答","sourceMessageIds":[]}`}
	agent, _ := newTestAgent(model, "test", 16384)
	if _, err := agent.ExecuteMain(context.Background(), input, "怎么除味？", nil); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Context ai.Context `json:"context"`
	}
	if err := json.Unmarshal([]byte(model.requests[0].Messages[1].Content), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Context.Messages) > aicontext.DefaultRecentMessages+1 || payload.Context.Request == nil {
		t.Fatal("unbounded initial context")
	}
	model.requests = nil
	input.Messages[len(input.Messages)-1].Text = strings.Repeat("超限", 10000)
	if _, err := agent.ExecuteMain(context.Background(), input, "帮忙", nil); !errors.Is(err, ai.ErrContext) || len(model.requests) != 0 {
		t.Fatal("oversized request reached model", err)
	}
}
func TestMainToolLoopHasAHardLimit(t *testing.T) {
	input := testContext()
	m := &mainTestModel{decision: `{"name":"continue","arguments":{}}`}
	calls := 0
	registry, _ := tools.New(tools.NewTyped(tools.Metadata{Name: "continue", Description: "test"}, func(context.Context, tools.Scope, struct{}) (struct {
		Value string `json:"value"`
	}, error) {
		calls++
		return struct {
			Value string `json:"value"`
		}{Value: "continue"}, nil
	}))
	a, _ := NewAgent(m, "test", 16384, registry)
	if _, err := a.ExecuteMain(context.Background(), input, "test", nil); err == nil || calls != MaxToolSteps {
		t.Fatal("unbounded tool loop", err, calls)
	}
}

func TestUnsupportedModesDoNotInvokeModel(t *testing.T) {
	for _, mode := range []string{"qa", "help", "extract", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			model := &mainTestModel{}
			agent, _ := newTestAgent(model, "test", 8192)
			if _, err := agent.Invoke(context.Background(), ai.Invocation{Context: testContext(), Mode: mode, Question: "问题"}); err == nil || len(model.requests) != 0 {
				t.Fatal("unsupported mode executed", err)
			}
		})
	}
}

func TestMainAnswersDirectlyInOneModelCall(t *testing.T) {
	for _, ids := range []string{`[]`, `["m1"]`} {
		model := &mainTestModel{decision: `{"name":"respond","arguments":{"status":"answered","answer":"这是直接回答。","sourceMessageIds":` + ids + `}}`}
		agent, err := newTestAgent(model, "test", 16384)
		if err != nil {
			t.Fatal(err)
		}
		run, err := agent.ExecuteMain(context.Background(), testContext(), "请直接回答", nil)
		if err != nil || run.Answer == nil || run.Answer.Answer != "这是直接回答。" || len(model.requests) != 1 || run.Route.Version != PromptVersion {
			t.Fatalf("direct response failed: %+v %v", run, err)
		}
		if _, text, err := ai.ResultFromRun(run); err != nil || text != "这是直接回答。" {
			t.Fatalf("final reply: %q %v", text, err)
		}
		if ids != `[]` && (len(run.Sources) != 1 || run.Sources[0].ID != "m1") {
			t.Fatal("validated source lost")
		}
	}
}

func TestMainDirectAnswerCannotInventSources(t *testing.T) {
	model := &mainTestModel{decision: `{"name":"respond","arguments":{"status":"answered","answer":"伪造的回答","sourceMessageIds":["another-group-message"]}}`}
	agent, err := newTestAgent(model, "test", 16384)
	if err != nil {
		t.Fatal(err)
	}
	run, err := agent.ExecuteMain(context.Background(), testContext(), "请回答", nil)
	if !errors.Is(err, ai.ErrInvalidDecision) || run.Answer != nil || len(model.requests) != MaxToolSteps+1 {
		t.Fatalf("unverified reply accepted: %+v %v", run, err)
	}
}
