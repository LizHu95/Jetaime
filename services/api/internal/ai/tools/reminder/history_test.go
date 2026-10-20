package remindertool

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/bootstrap"
	aicontext "github.com/LizHu95/Jetaime/services/api/internal/ai/context"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/qa"
	"github.com/LizHu95/Jetaime/services/api/internal/reminder"
)

type historyReminderAPI struct {
	create reminder.CreateInput
	patch  reminder.UpdateInput
}

func (s *historyReminderAPI) Create(_ context.Context, _ reminder.Scope, input reminder.CreateInput, _ reminder.Operation) (reminder.Result, error) {
	s.create = input
	return s.result(input.Title, input.Date, input.Time), nil
}
func (s *historyReminderAPI) Update(_ context.Context, _ reminder.Scope, _ string, input reminder.UpdateInput, _ reminder.Operation) (reminder.Result, error) {
	s.patch = input
	clock := ""
	if input.Time != nil {
		clock = *input.Time
	}
	return s.result("面试goodsnote", "2026-10-23", clock), nil
}
func (*historyReminderAPI) CancelDraft(context.Context, reminder.Scope, reminder.Operation) (reminder.Result, error) {
	panic("unexpected cancel")
}
func (*historyReminderAPI) Draft(context.Context, reminder.Scope) (*reminder.Reminder, error) {
	return nil, nil
}
func (*historyReminderAPI) result(title, date, clock string) reminder.Result {
	r := &reminder.Reminder{Title: title, Date: date, Time: clock}
	if clock == "" {
		return reminder.Result{Status: "needs_clarification", Reminder: r, MissingFields: []string{"time"}}
	}
	due, _ := time.ParseInLocation("2006-01-02 15:04", date+" "+clock, time.FixedZone("Asia/Shanghai", 8*3600))
	r.DueAt = &due
	return reminder.Result{Status: "created", Reminder: r}
}

type historyReminderModel struct {
	t     *testing.T
	calls int
}

func (m *historyReminderModel) Call(_ context.Context, request llm.Request) (llm.Result, error) {
	m.calls++
	var payload struct {
		Context ai.Context         `json:"context"`
		Tools   []tools.Definition `json:"tools"`
	}
	if err := json.Unmarshal([]byte(request.Messages[1].Content), &payload); err != nil {
		m.t.Fatal(err)
	}
	found := false
	for _, definition := range payload.Tools {
		if definition.Name == "set_reminder" {
			found = true
			var schema struct {
				Properties map[string]json.RawMessage `json:"properties"`
			}
			if err := json.Unmarshal(definition.InputSchema, &schema); err != nil || schema.Properties["sourceMessageIds"] == nil {
				m.t.Fatal("reminder source argument was not registered", err)
			}
		}
	}
	if !found {
		m.t.Fatal("set_reminder not registered")
	}
	if len(payload.Context.Messages) != 2 || payload.Context.Messages[0].Text != "本周五下午15:30面试goodsnote" {
		m.t.Fatal("historical schedule missing from model input")
	}
	return llm.Result{Content: `{"name":"set_reminder","arguments":{"action":"save","title":"面试goodsnote","date":"2026-10-23","time":"15:30","sourceMessageIds":["schedule","request"]}}`}, nil
}
func historyContext(t *testing.T) ai.Context {
	t.Helper()
	at := time.Date(2026, 10, 20, 22, 0, 35, 0, time.FixedZone("Asia/Shanghai", 8*3600))
	input := ai.Context{GroupID: "g", ActorID: "u", Users: []ai.User{{ID: "u", Nickname: "L"}}, Messages: []ai.Message{
		{ID: "schedule", GroupID: "g", SenderID: "u", Text: "本周五下午15:30面试goodsnote", SentAt: at},
		{ID: "request", GroupID: "g", SenderID: "u", Text: "提醒我面试", SentAt: at.Add(13 * time.Second)},
	}}
	selected, err := aicontext.InjectDefaultContext(context.Background(), aicontext.ContextRequest{Snapshot: input, TriggerID: "request", Question: "提醒我面试", Now: at.Add(13 * time.Second), Tokens: 16384})
	if err != nil {
		t.Fatal(err)
	}
	return selected
}
func TestRegisteredReminderUsesHistoricalTime(t *testing.T) {
	for _, draft := range []bool{false, true} {
		t.Run(map[bool]string{false: "create", true: "complete draft"}[draft], func(t *testing.T) {
			input := historyContext(t)
			if draft {
				input.ReminderDraft = &ai.ReminderDraft{ID: "draft", Version: 1, Title: "面试goodsnote", Date: "2026-10-23"}
			}
			api := &historyReminderAPI{}
			adapter := New(api, func(tools.Scope) (reminder.Operation, error) { return reminder.Operation{}, nil })
			model := &historyReminderModel{t: t}
			registry, err := tools.New(qa.New(model, "test", 16384, nil), adapter)
			if err != nil {
				t.Fatal(err)
			}
			service, err := bootstrap.New(model, "test", 16384, registry)
			if err != nil {
				t.Fatal(err)
			}
			run, err := service.Execute(context.Background(), ai.Invocation{Context: input, Question: "提醒我面试"})
			if err != nil {
				t.Fatal(err)
			}
			if model.calls != 1 || run.Answer == nil || run.Answer.Status != "answered" {
				t.Fatalf("reminder still asks for time: %+v", run.Answer)
			}
			if draft {
				if api.patch.Time == nil || *api.patch.Time != "15:30" {
					t.Fatalf("historical time lost updating draft: %+v", api.patch)
				}
			} else if api.create.Time != "15:30" {
				t.Fatalf("historical time lost creating reminder: %+v", api.create)
			}
			if len(run.Answer.SourceMessageIDs) != 2 || run.Answer.SourceMessageIDs[1] != "schedule" {
				t.Fatalf("schedule evidence missing: %v", run.Answer.SourceMessageIDs)
			}
		})
	}
}
func TestHistoricalTimeRequiresCitedUserEvidence(t *testing.T) {
	input := historyContext(t)
	if hasExplicitTime(input, "15:30") {
		t.Fatal("uncited background time used")
	}
	if !hasExplicitTime(input, "15:30", "schedule") {
		t.Fatal("explicit historical time ignored")
	}
	for _, tc := range []struct {
		name, id, sender, group string
		future                  bool
	}{
		{name: "missing", id: "missing", sender: "u", group: "g"},
		{name: "peer", id: "schedule", sender: "peer", group: "g"},
		{name: "assistant", id: "schedule", sender: "ai", group: "g"},
		{name: "foreign group", id: "schedule", sender: "u", group: "other"},
		{name: "future", id: "schedule", sender: "u", group: "g", future: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := historyContext(t)
			input.Messages[0].SenderID = tc.sender
			input.Messages[0].GroupID = tc.group
			if tc.future {
				input.Messages[0].SentAt = input.Request.SentAt.Add(time.Second)
			}
			if _, err := reminderSources(input, []string{tc.id}); !errors.Is(err, ai.ErrInvalidDecision) {
				t.Fatalf("invalid evidence accepted: %v", err)
			}
		})
	}
}
