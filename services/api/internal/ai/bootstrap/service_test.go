package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/qa"
)

type disabledExtractionModel struct {
	calls int
}

func (m *disabledExtractionModel) Call(_ context.Context, request llm.Request) (llm.Result, error) {
	m.calls++
	var payload struct {
		Tools []tools.Definition `json:"tools"`
	}
	if err := json.Unmarshal([]byte(request.Messages[1].Content), &payload); err != nil {
		return llm.Result{}, err
	}
	if len(payload.Tools) != 1 || payload.Tools[0].Name != "answer_question" {
		return llm.Result{}, errors.New("unexpected explicit tool catalog")
	}
	return llm.Result{Content: `{"name":"extract_todos","arguments":{"question":"整理待办"}}`}, nil
}

func TestExplicitQAToolsRejectExtraction(t *testing.T) {
	model := &disabledExtractionModel{}
	registry, err := tools.New(qa.New(model, "test", 16384, nil))
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(model, "test", 16384, registry)
	if err != nil {
		t.Fatal(err)
	}
	input := ai.Context{GroupID: "g", ActorID: "u", Users: []ai.User{{ID: "u", Nickname: "用户"}}, Messages: []ai.Message{{ID: "m", GroupID: "g", SenderID: "u", Text: "整理待办", SentAt: time.Now()}}}
	_, err = service.Execute(context.Background(), ai.Invocation{Context: input, Question: "整理待办", LoadExtraction: func(context.Context) (ai.Context, error) {
		t.Fatal("disabled tool loaded extraction context")
		return input, nil
	}})
	if !errors.Is(err, ai.ErrInvalidDecision) || model.calls != 3 {
		t.Fatalf("disabled extraction executed: calls=%d err=%v", model.calls, err)
	}
}

type customCatalogModel struct{ t *testing.T }

func (m customCatalogModel) Call(_ context.Context, request llm.Request) (llm.Result, error) {
	var payload struct {
		Tools []tools.Definition `json:"tools"`
	}
	if err := json.Unmarshal([]byte(request.Messages[1].Content), &payload); err != nil {
		return llm.Result{}, err
	}
	if len(payload.Tools) != 1 || payload.Tools[0].Name != "echo" {
		m.t.Fatalf("bootstrap added an implicit capability: %+v", payload.Tools)
	}
	return llm.Result{Content: `{"name":"echo","arguments":{"text":"ready"}}`}, nil
}

func TestBootstrapUsesOnlySuppliedRegistry(t *testing.T) {
	type input struct {
		Text string `json:"text"`
	}
	type output struct {
		Text string `json:"text"`
	}
	tool := tools.NewTyped(tools.Metadata{Name: "echo", Description: "回显", Terminal: true}, func(_ context.Context, _ tools.Scope, in input) (output, error) { return output{Text: in.Text}, nil }, tools.Options[output]{Present: func(out output) tools.Completion {
		return tools.Completion{Answer: &ai.Answer{Status: "answered", Answer: out.Text, SourceMessageIDs: []string{}}}
	}})
	registry, err := tools.New(tool)
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(customCatalogModel{t: t}, "test", 16384, registry)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := ai.Context{GroupID: "g", ActorID: "u", Users: []ai.User{{ID: "u", Nickname: "User"}}, Messages: []ai.Message{{ID: "m", GroupID: "g", SenderID: "u", Text: "echo", SentAt: time.Now()}}}
	run, err := service.Execute(context.Background(), ai.Invocation{Context: snapshot, Question: "echo"})
	if err != nil || run.Answer == nil || run.Answer.Answer != "ready" {
		t.Fatalf("explicit registry not used: %+v %v", run.Answer, err)
	}
	if definitions := registry.Definitions(); len(definitions) != 1 || definitions[0].Name != "echo" {
		t.Fatal("supplied registry mutated")
	}
}

func TestBootstrapRequiresExplicitRegistry(t *testing.T) {
	if _, err := New(customCatalogModel{t: t}, "test", 16384, nil); err == nil {
		t.Fatal("missing registry silently replaced with defaults")
	}
}
