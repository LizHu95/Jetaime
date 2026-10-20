package extracttool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
)

type stubModel struct {
	content       string
	request       llm.Request
	reviewContent string
}

func (m *stubModel) Call(_ context.Context, r llm.Request) (llm.Result, error) {
	if strings.HasPrefix(r.Messages[0].Content, actionReviewPrompt) {
		if m.reviewContent != "" {
			return llm.Result{Content: m.reviewContent}, nil
		}
		return llm.Result{Content: `{"reviews":[{"candidateIndex":0,"status":"active","evidenceMessageIds":["m6"],"reason":"明确安排且未完成"}]}`}, nil
	}
	m.request = r
	return llm.Result{Content: m.content, Raw: map[string]string{"content": m.content}}, nil
}
func setup(t *testing.T, content string) (ai.Context, *Tool, *stubModel) {
	t.Helper()
	at := time.Date(2026, 10, 10, 10, 0, 0, 0, time.FixedZone("Asia/Shanghai", 8*3600))
	input := ai.Context{GroupID: "daily", ActorID: "lin", Users: []ai.User{{ID: "lin", Nickname: "测试用户 A"}, {ID: "xia", Nickname: "测试用户 B"}}, Tasks: []ai.Task{}, Messages: []ai.Message{
		{ID: "m1", GroupID: "daily", SenderID: "lin", SentAt: at, Text: "测试消息"},
		{ID: "m6", GroupID: "daily", SenderID: "lin", SentAt: at.Add(time.Minute), Text: "我明天负责测试任务"},
	}}
	model := &stubModel{content: content}
	agent := New(model, "test", 8192)
	return input, agent, model
}

func executeExtractionTool(ctx context.Context, agent *Tool, input ai.Context, question string) (ai.Run, error) {
	if question == "" {
		question = "提取当前对话的待办"
	}
	raw, _ := json.Marshal(map[string]string{"question": question})
	registry, err := tools.New(agent)
	if err != nil {
		return ai.Run{}, err
	}
	result, err := registry.Execute(ctx, tools.Scope{Context: input}, tools.Call{Name: "extract_todos", Arguments: raw})
	return result.Run, err
}
