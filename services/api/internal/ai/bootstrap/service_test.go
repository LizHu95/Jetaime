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
		return llm.Result{}, errors.New("unexpected default tool catalog")
	}
	return llm.Result{Content: `{"name":"extract_todos","arguments":{"question":"整理待办"}}`}, nil
}

func TestDefaultToolsRejectExtraction(t *testing.T) {
	model := &disabledExtractionModel{}
	service, err := New(model, "test", 16384)
	if err != nil {
		t.Fatal(err)
	}
	input := ai.Context{GroupID: "g", ActorID: "u", Users: []ai.User{{ID: "u", Nickname: "用户"}}, Messages: []ai.Message{{ID: "m", GroupID: "g", SenderID: "u", Text: "整理待办", SentAt: time.Now()}}}
	_, err = service.Execute(context.Background(), ai.Invocation{Context: input, Question: "整理待办", LoadExtraction: func(context.Context) (ai.Context, error) {
		t.Fatal("disabled tool loaded extraction context")
		return input, nil
	}})
	if !errors.Is(err, ai.ErrInvalidDecision) || model.calls != 1 {
		t.Fatalf("disabled extraction executed: calls=%d err=%v", model.calls, err)
	}
}
