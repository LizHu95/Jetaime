package debugconsole

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LizHu95/Jetaime/services/api/internal/ai/bootstrap"
	"github.com/LizHu95/Jetaime/services/api/internal/llm"
)

type stubModel struct {
	content       string
	request       llm.Request
	reviewContent string
}

func (m *stubModel) Call(_ context.Context, r llm.Request) (llm.Result, error) {
	if strings.Contains(string(r.Format), `"reviews"`) {
		if m.reviewContent != "" {
			return llm.Result{Content: m.reviewContent}, nil
		}
		return llm.Result{Content: `{"reviews":[{"candidateIndex":0,"status":"active","evidenceMessageIds":["m6"],"reason":"明确安排且未完成"}]}`}, nil
	}
	m.request = r
	return llm.Result{Content: m.content, Raw: map[string]string{"content": m.content}}, nil
}
func setup(t *testing.T, content string) (*Store, *Workflow, *stubModel) {
	t.Helper()
	dir := t.TempDir()
	fixture := filepath.Join(dir, "history.json")
	// Minimal test-only records; never loaded by the debug console.
	data := `{"users":[{"id":"lin","nickname":"测试用户 A"},{"id":"xia","nickname":"测试用户 B"},{"id":"chen","nickname":"测试用户 C"}],"groups":[{"id":"daily","name":"测试群 A"},{"id":"other","name":"测试群 B"}],"members":[{"groupId":"daily","userId":"lin"},{"groupId":"daily","userId":"xia"},{"groupId":"other","userId":"chen"}],"messages":[{"id":"m1","groupId":"daily","senderId":"lin","sentAt":"2026-10-10T10:00:00+08:00","text":"测试消息"},{"id":"m6","groupId":"daily","senderId":"lin","sentAt":"2026-10-10T10:01:00+08:00","text":"我明天负责测试任务"},{"id":"m8","groupId":"other","senderId":"chen","sentAt":"2026-10-10T10:02:00+08:00","text":"隔离测试消息"}]}`
	if err := os.WriteFile(fixture, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(fixture, filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	m := &stubModel{content: content}
	executor, err := bootstrap.New(m, "test", 8192)
	if err != nil {
		t.Fatal(err)
	}
	w, err := NewWorkflow(s, executor, "test", "")
	if err != nil {
		t.Fatal(err)
	}
	return s, w, m
}
func TestGroupIsolationAndSourceValidation(t *testing.T) {
	_, w, m := setup(t, `{"status":"answered","answer":"隔离测试消息","sourceMessageIds":["m8"]}`)
	run, err := w.Execute(context.Background(), "daily", "lin", "qa", "测试问题？", "")
	if err == nil || run.Answer != nil || run.Status != "failed" {
		t.Fatalf("cross-group source accepted: %+v %v", run, err)
	}
	if strings.Contains(m.request.Messages[1].Content, `"m8"`) {
		t.Fatal("other group leaked into context")
	}
	if _, err = w.Execute(context.Background(), "daily", "chen", "qa", "测试问题？", ""); err == nil {
		t.Fatal("non-member allowed")
	}
}
func TestConfirmPersistenceIdempotenceAndOwnership(t *testing.T) {
	s, w, _ := setup(t, `{"suggestions":[{"title":"测试任务","assigneeId":"lin","timeExpression":"明天","sourceMessageIds":["m6"]}]}`)
	r, err := w.Execute(context.Background(), "daily", "lin", "extract", "", "")
	if err != nil {
		t.Fatal(err)
	}
	c, _ := s.Context("daily", "lin")
	if len(c.Tasks) != 0 {
		t.Fatal("extraction wrote tasks")
	}
	sug := r.Suggestions[0]
	task, err := s.Confirm("daily", "lin", sug.ID, sug, sug.Title, "lin", sug.DueAt)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(s.fixturePath, s.statePath)
	if err != nil {
		t.Fatal(err)
	}
	again, err := reopened.Confirm("daily", "lin", sug.ID, sug, sug.Title, "lin", sug.DueAt)
	if err != nil || again.ID != task.ID {
		t.Fatalf("confirmation not idempotent: %v", err)
	}
	if _, err = reopened.Complete("daily", "xia", task.ID, true); err == nil {
		t.Fatal("non-assignee completed task")
	}
	if _, err = reopened.Complete("daily", "lin", task.ID, true); err != nil {
		t.Fatal(err)
	}
}
func TestFailedPersistenceDoesNotChangeMemory(t *testing.T) {
	s, _, _ := setup(t, "")
	before, _ := s.Context("daily", "lin")
	block := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(block, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	s.statePath = filepath.Join(block, "state.json")
	if _, err := s.AppendMessage("daily", "lin", "不能保存"); err == nil {
		t.Fatal("expected disk failure")
	}
	after, _ := s.Context("daily", "lin")
	if len(after.Messages) != len(before.Messages) {
		t.Fatal("failed write changed memory")
	}
}
func TestLocalHTTPBoundary(t *testing.T) {
	_, w, _ := setup(t, "")
	s := NewServer(w)
	for _, tc := range []struct {
		host, origin, path string
		status             int
	}{{"evil.example", "", "/api/assistant/catalog", 403}, {"127.0.0.1:8081", "https://evil.example", "/api/assistant/catalog", 403}, {"127.0.0.1:8081", "", "/api/assistant/state?groupId=daily&actorId=chen", 403}, {"127.0.0.1:8081", "", "/api/assistant/state?groupId=daily&actorId=lin", 200}} {
		r := httptest.NewRequest("GET", tc.path, nil)
		r.Host = tc.host
		r.Header.Set("Origin", tc.origin)
		out := httptest.NewRecorder()
		s.ServeHTTP(out, r)
		if out.Code != tc.status {
			t.Fatalf("%+v: %d %s", tc, out.Code, out.Body.String())
		}
	}
}
