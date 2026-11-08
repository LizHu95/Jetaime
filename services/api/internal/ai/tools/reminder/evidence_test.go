package remindertool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	mainagent "github.com/LizHu95/Jetaime/services/api/internal/ai/agent/main"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/amap"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/respond"
	"github.com/LizHu95/Jetaime/services/api/internal/reminder"
)

func TestRecipientBoundaryBeforeOperation(t *testing.T) {
	for _, tc := range []struct {
		text    string
		blocked bool
	}{
		{"今天22:43提醒张三给洋芋换水", true}, {"15:30提醒@张三喝水", true}, {"15:30提醒他喝水", true}, {"给张三设个提醒，15:30喝水", true},
		{"15:30提醒我联系张三", false}, {"15:30提醒我提醒张三换水", false}, {"15:30提醒我喝水", false},
	} {
		t.Run(tc.text, func(t *testing.T) {
			input := historyContext(t)
			input.Users = append(input.Users, ai.User{ID: "peer", Nickname: "张三"})
			input.Messages[1].Text = tc.text
			api, operations := &historyReminderAPI{}, 0
			tool := New(api, func(tools.Scope) (reminder.Operation, error) { operations++; return reminder.Operation{}, nil })
			args := Arguments{Action: "save", Title: "喝水", Date: "2026-10-23", Time: "15:30", SourceMessageIDs: []string{"request"}}
			out, err := tool.Execute(context.Background(), tools.Scope{Context: input}, args)
			if err != nil {
				t.Fatal(err)
			}
			if tc.blocked {
				if operations != 0 || api.create.Title != "" || out.Result.Reason != "recipient_not_supported" || !strings.Contains(reply(out.Result), "本人") {
					t.Fatalf("recipient request reached write path: %+v", out)
				}
			} else if operations != 1 || api.create.Title != "喝水" {
				t.Fatal("self reminder blocked")
			}
		})
	}
}

func TestWrongHistoricalClockCannotWriteAndCanBeRepaired(t *testing.T) {
	input := historyContext(t)
	api := &historyReminderAPI{}
	tool := New(api, func(tools.Scope) (reminder.Operation, error) { return reminder.Operation{}, nil })
	args := Arguments{Action: "save", Title: "面试goodsnote", Date: "2026-10-23", Time: "15:00", SourceMessageIDs: []string{"schedule", "request"}}
	raw, _ := json.Marshal(args)
	_, err := tool.Invoke(context.Background(), tools.Scope{Context: input}, raw)
	if _, repairable := tools.Repairable(err); !repairable || api.create.Title != "" {
		t.Fatal("15:30 changed to 15:00 and written", err)
	}
	args.Time = "15:30"
	raw, _ = json.Marshal(args)
	if _, err := tool.Invoke(context.Background(), tools.Scope{Context: input}, raw); err != nil || api.create.Time != "15:30" {
		t.Fatal("valid repair rejected", err)
	}
	args.Time = ""
	raw, _ = json.Marshal(args)
	api.create = reminder.CreateInput{}
	if _, err := tool.Invoke(context.Background(), tools.Scope{Context: input}, raw); err == nil || api.create.Title != "" {
		t.Fatal("known clock discarded into draft")
	}
	input.Messages[1].Text = "提前30分钟提醒我面试"
	if !hasExplicitTime(input, "15:00", "schedule") || hasExplicitTime(input, "15:30", "schedule") {
		t.Fatal("explicit adjustment lost")
	}
	input.ReminderDraft = &ai.ReminderDraft{Time: "15:30"}
	input.Messages[1].Text = "改成下午四点"
	if hasExplicitTime(input, "15:30") || !hasExplicitTime(input, "16:00") {
		t.Fatal("old draft overrode new explicit time")
	}
}

func TestFullCatalogPreservesImmediateScheduleOverSoftReserve(t *testing.T) {
	api := &historyReminderAPI{}
	adapter := New(api, func(tools.Scope) (reminder.Operation, error) { return reminder.Operation{}, nil })
	entries := []tools.Tool{respond.New(), adapter}
	entries = append(entries, amap.New(nil, nil)...)
	registry, err := tools.New(entries...)
	if err != nil {
		t.Fatal(err)
	}
	model := &historyReminderModel{t: t}
	agent, err := mainagent.NewAgent(model, "test", 32768, registry)
	if err != nil {
		t.Fatal(err)
	}
	run, err := agent.ExecuteMain(context.Background(), historyContext(t), "提醒我面试", nil)
	if err != nil || run.Answer == nil || api.create.Time != "15:30" {
		t.Fatal("catalog budget lost schedule", err)
	}
}
