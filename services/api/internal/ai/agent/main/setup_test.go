package mainagent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/debugconsole"
	"github.com/LizHu95/Jetaime/services/api/internal/llm"
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
func setup(t *testing.T, content string) (*debugconsole.Store, *testWorkflow, *stubModel) {
	t.Helper()
	dir := t.TempDir()
	fixture := filepath.Join(dir, "history.json")
	// Minimal test-only records; never loaded by the debug console.
	data := `{"users":[{"id":"lin","nickname":"测试用户 A"},{"id":"xia","nickname":"测试用户 B"},{"id":"chen","nickname":"测试用户 C"}],"groups":[{"id":"daily","name":"测试群 A"},{"id":"other","name":"测试群 B"}],"members":[{"groupId":"daily","userId":"lin"},{"groupId":"daily","userId":"xia"},{"groupId":"other","userId":"chen"}],"messages":[{"id":"m1","groupId":"daily","senderId":"lin","sentAt":"2026-10-10T10:00:00+08:00","text":"测试消息"},{"id":"m6","groupId":"daily","senderId":"lin","sentAt":"2026-10-10T10:01:00+08:00","text":"我明天负责测试任务"},{"id":"m8","groupId":"other","senderId":"chen","sentAt":"2026-10-10T10:02:00+08:00","text":"隔离测试消息"}]}`
	if err := os.WriteFile(fixture, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := debugconsole.OpenStore(fixture, filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	m := &stubModel{content: content}
	agent, err := NewAgent(m, "test", 8192)
	if err != nil {
		t.Fatal(err)
	}
	return s, &testWorkflow{Agent: agent, Store: s}, m
}

type testWorkflow struct {
	*Agent
	Store *debugconsole.Store
}

func (w *testWorkflow) Execute(ctx context.Context, group, actor, kind, question, model string) (ai.Run, error) {
	input, err := w.Store.Context(group, actor)
	if err != nil {
		return ai.Run{}, err
	}
	return w.Agent.Execute(ctx, input, kind, question)
}
