package remindertool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/bootstrap"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/qa"
	"github.com/LizHu95/Jetaime/services/api/internal/reminder"
)

type clarificationAPI struct {
	historyReminderAPI
	updates int
}

func (s *clarificationAPI) Update(_ context.Context, _ reminder.Scope, id string, in reminder.UpdateInput, _ reminder.Operation) (reminder.Result, error) {
	s.updates++
	s.patch = in
	date := ""
	if in.Date != nil {
		date = *in.Date
	}
	return reminder.Result{Status: "needs_clarification", ReminderID: id, Reminder: &reminder.Reminder{ID: id, Title: "吃饭", Date: date, Status: reminder.Draft}, MissingFields: []string{"time"}}, nil
}

type clarificationModel struct {
	t     *testing.T
	calls int
}

func (m *clarificationModel) Call(_ context.Context, req llm.Request) (llm.Result, error) {
	m.calls++
	sources := []string{"request", "assistant-question"}
	if m.calls == 2 {
		if len(req.Messages) != 3 || !strings.Contains(req.Messages[2].Content, "invalid_sources") || !strings.Contains(req.Messages[2].Content, "assistant-question") || !strings.Contains(req.Messages[2].Content, "当前可用的真人消息 ID") {
			m.t.Fatal("feedback does not identify the rejected source and eligible messages")
		}
		sources = []string{"request"}
	}
	raw, err := json.Marshal(map[string]any{"name": "set_reminder", "arguments": Arguments{Action: "save", Title: "吃饭", Date: "2026-10-21", Time: "", SourceMessageIDs: sources}})
	return llm.Result{Content: string(raw)}, err
}
func TestDateClarificationRepairsAssistantCitationWithoutInventingTime(t *testing.T) {
	at := time.Date(2026, 10, 20, 22, 42, 38, 0, time.FixedZone("Asia/Shanghai", 8*3600))
	input := ai.Context{GroupID: "g", ActorID: "u", Users: []ai.User{{ID: "u", Nickname: "User"}, {ID: "assistant", Nickname: "AI"}}, Messages: []ai.Message{
		{ID: "unrelated-time", GroupID: "g", SenderID: "u", Text: "今天22:43提醒我换水", SentAt: at.Add(-time.Minute)},
		{ID: "original", GroupID: "g", SenderID: "u", Text: "提醒我吃饭", SentAt: at.Add(-20 * time.Second)},
		{ID: "assistant-question", GroupID: "g", SenderID: "assistant", Text: "你想哪天、几点提醒你吃饭？", SentAt: at.Add(-10 * time.Second)},
		{ID: "request", GroupID: "g", SenderID: "u", Text: "明天", SentAt: at},
	}, Request: &ai.RequestContext{MessageID: "request", SenderID: "u", SentAt: at, CurrentTime: at, Timezone: "Asia/Shanghai"}, ReminderDraft: &ai.ReminderDraft{ID: "draft", Title: "吃饭", Version: 1}}
	api := &clarificationAPI{}
	model := &clarificationModel{t: t}
	adapter := New(api, func(tools.Scope) (reminder.Operation, error) { return reminder.Operation{}, nil })
	registry, err := tools.New(qa.New(model, "test", 16384, nil), adapter)
	if err != nil {
		t.Fatal(err)
	}
	executor, err := bootstrap.New(model, "test", 16384, registry)
	if err != nil {
		t.Fatal(err)
	}
	run, err := executor.Execute(context.Background(), ai.Invocation{Context: input, Question: "明天"})
	if err != nil || model.calls != 2 || api.updates != 1 {
		t.Fatalf("date follow-up failed or invalid source caused a write: calls=%d writes=%d err=%v", model.calls, api.updates, err)
	}
	if api.patch.Date == nil || *api.patch.Date != "2026-10-21" || api.patch.Time != nil {
		t.Fatalf("date lost or unrelated time borrowed: %+v", api.patch)
	}
	if run.Answer == nil || run.Answer.Status != "insufficient_evidence" || !strings.Contains(run.Answer.Answer, "2026-10-21 几点") || len(run.Answer.SourceMessageIDs) != 1 || run.Answer.SourceMessageIDs[0] != "request" {
		t.Fatalf("wrong clarification or sources: %+v", run.Answer)
	}
}
