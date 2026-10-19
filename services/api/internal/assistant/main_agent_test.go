package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type mainTestModel struct {
	decision string
	requests []ModelRequest
}

func (m *mainTestModel) Call(_ context.Context, request ModelRequest) (ModelResult, error) {
	m.requests = append(m.requests, request)
	if strings.HasPrefix(request.Messages[0].Content, mainPrompt) {
		return ModelResult{Content: m.decision}, nil
	}
	return ModelResult{Content: `{"suggestions":[]}`}, nil
}

func TestMainAnswersDirectlyWithoutLoadingChild(t *testing.T) {
	for _, basis := range []string{"general", "history", "mixed"} {
		t.Run(basis, func(t *testing.T) {
			store, _, _ := setup(t, `{}`)
			input, _ := store.Context("daily", "lin")
			sources := `[]`
			if basis != "general" {
				sources = `["m1"]`
			}
			model := &mainTestModel{decision: `{"action":"answer","basis":"` + basis + `","answer":{"status":"answered","answer":"回答","sourceMessageIds":` + sources + `}}`}
			agent, _ := NewAgent(model, "test", 16384)
			run, err := agent.ExecuteMain(context.Background(), input, "请帮忙", func(context.Context) (Context, error) {
				t.Fatal("ordinary answer loaded child context")
				return input, nil
			})
			if err != nil || run.Kind != "main" || run.PromptVersion != "main-v1" || run.Answer == nil || len(model.requests) != 1 {
				t.Fatal("main did not answer directly", run, err)
			}
			result, _, err := ResultFromRun(run)
			if err != nil || result.Basis != basis || result.Route == nil || result.Route.Kind != "main" {
				t.Fatal("main result metadata lost", err)
			}
		})
	}
}

func TestMainDelegatesOnlyToExtraction(t *testing.T) {
	store, _, _ := setup(t, `{}`)
	input, _ := store.Context("daily", "lin")
	model := &mainTestModel{decision: `{"action":"extract","basis":"none","answer":null}`}
	agent, _ := NewAgent(model, "test", 16384)
	loads := 0
	run, err := agent.ExecuteMain(context.Background(), input, "整理今天的待办", func(context.Context) (Context, error) { loads++; return input, nil })
	if err != nil || loads != 1 || len(model.requests) != 2 || run.Kind != "extract" || run.Route == nil || run.Route.Version != "main-v1" {
		t.Fatal("extraction delegation failed", run, err)
	}
	if !strings.Contains(model.requests[1].Messages[1].Content, "整理今天的待办") || !strings.Contains(model.requests[1].Messages[0].Content, "不扩大范围") {
		t.Fatal("child extraction scope lost")
	}
}

func TestMainRejectsInvalidDecisionsBeforeLoadingChild(t *testing.T) {
	for _, decision := range []string{
		`not json`,
		`{"action":"delete","basis":"none","answer":null}`,
		`{"action":"extract","basis":"none","answer":null,"extra":true}`,
		`{"action":"extract","basis":"none"}`,
		`{"action":"answer","basis":"general","answer":null}`,
		`{"action":"answer","basis":"history","answer":{"status":"answered","answer":"无依据","sourceMessageIds":[]}}`,
		`{"action":"answer","basis":"general","answer":{"status":"answered","answer":"伪造引用","sourceMessageIds":["foreign"]}}`,
	} {
		store, _, _ := setup(t, `{}`)
		input, _ := store.Context("daily", "lin")
		model := &mainTestModel{decision: decision}
		agent, _ := NewAgent(model, "test", 16384)
		_, err := agent.ExecuteMain(context.Background(), input, "帮忙", func(context.Context) (Context, error) { t.Fatal("invalid decision loaded child"); return input, nil })
		if !errors.Is(err, ErrMainDecision) || len(model.requests) != 1 {
			t.Fatal("invalid decision accepted", err)
		}
	}
}

func TestMainContextAndExtensionsCannotBypassBudget(t *testing.T) {
	store, _, _ := setup(t, `{}`)
	input, _ := store.Context("daily", "lin")
	seed := input.Messages[0]
	for i := 0; i < 30; i++ {
		message := seed
		message.ID = string(rune('A' + i))
		input.Messages = append(input.Messages, message)
	}
	model := &mainTestModel{decision: `{"action":"answer","basis":"general","answer":{"status":"answered","answer":"回答","sourceMessageIds":[]}}`}
	agent, _ := NewAgent(model, "test", 16384)
	if _, err := agent.ExecuteMain(context.Background(), input, "怎么除味？", nil); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Context Context `json:"context"`
	}
	if err := json.Unmarshal([]byte(model.requests[0].Messages[1].Content), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Context.Messages) > DefaultRecentMessages+1 || payload.Context.Request == nil {
		t.Fatal("initial context missing or unbounded")
	}
	model.requests = nil
	input.Messages[len(input.Messages)-1].Text = strings.Repeat("超限", 10000)
	if _, err := agent.ExecuteMain(context.Background(), input, "帮忙", nil); !errors.Is(err, ErrBudget) || len(model.requests) != 0 {
		t.Fatal("oversized request called model", err)
	}
	input, _ = store.Context("daily", "lin")
	agent, _ = NewAgent(model, "test", 16384, WithMainInstructions(func(context.Context, Context, string) (string, error) {
		return strings.Repeat("扩展规则", 10000), nil
	}))
	if _, err := agent.ExecuteMain(context.Background(), input, "帮忙", nil); !errors.Is(err, ErrBudget) || len(model.requests) != 0 {
		t.Fatal("instruction extension bypassed budget", err)
	}
}

func TestMainRejectsForeignChildContext(t *testing.T) {
	store, _, _ := setup(t, `{}`)
	input, _ := store.Context("daily", "lin")
	model := &mainTestModel{decision: `{"action":"extract","basis":"none","answer":null}`}
	agent, _ := NewAgent(model, "test", 16384)
	_, err := agent.ExecuteMain(context.Background(), input, "整理待办", func(context.Context) (Context, error) { child := input; child.GroupID = "other"; return child, nil })
	if err == nil || len(model.requests) != 1 {
		t.Fatal("cross-group delegation accepted")
	}
}
