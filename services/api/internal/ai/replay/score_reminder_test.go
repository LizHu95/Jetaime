package replay

import (
	"encoding/json"
	"testing"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
	remindertool "github.com/LizHu95/Jetaime/services/api/internal/ai/tools/reminder"
	"github.com/LizHu95/Jetaime/services/api/internal/reminder"
)

func TestReminderScoringRejectsWrongWriteParametersAndState(t *testing.T) {
	date, clock := "2026-10-23", "15:30"
	for _, kind := range []string{"correct", "wrong time", "wrong date", "wrong title", "missing source", "wrong owner", "wrong state", "wrong saved time", "duplicate write", "no result", "missing call"} {
		t.Run(kind, func(t *testing.T) {
			c := Case{Reviewed: true, Expected: Expected{Reminder: &ExpectedReminder{Operations: 1, Action: "save", Date: &date, Time: &clock, TitleContainsAll: []string{"goodsnote", "面试"}, SourceMessageIDs: []string{"schedule", "request"}, ResultStatus: "created", ReminderStatus: "pending", UserID: "actor"}}}
			args := remindertool.Arguments{Action: "save", Title: "GoodsNote面试", Date: date, Time: clock, SourceMessageIDs: []string{"schedule", "request"}}
			business := reminder.Result{Status: "created", Reminder: &reminder.Reminder{UserID: "actor", Status: "pending", Date: date, Time: clock, Title: args.Title}}
			r := Result{Status: "succeeded", SimulatedReminderOperations: 1}
			switch kind {
			case "wrong time":
				args.Time = "15:00"
			case "wrong date":
				args.Date = "2026-10-24"
			case "wrong title":
				args.Title = "吃饭"
			case "missing source":
				args.SourceMessageIDs = []string{"request"}
			case "wrong owner":
				business.Reminder.UserID = "peer"
			case "wrong saved time":
				business.Reminder.Time = "15:00"
			case "wrong state":
				business.Status = "needs_clarification"
				business.Reminder.Status = "draft"
			case "duplicate write":
				r.SimulatedReminderOperations = 2
			}
			raw, _ := json.Marshal(args)
			call := tools.Call{Name: "set_reminder", Arguments: raw}
			if kind != "missing call" {
				r.Run.Steps = append(r.Run.Steps, ai.Step{Name: "tool_call", Status: "succeeded", Output: call})
			}
			if kind != "no result" {
				r.Run.Steps = append(r.Run.Steps, ai.Step{Name: "tool_result", Status: "succeeded", Input: call, Output: remindertool.Output{Result: business, SourceMessageIDs: args.SourceMessageIDs}})
			}
			scores := Score(c, r)
			all := true
			for _, value := range scores {
				all = all && value
			}
			if all != (kind == "correct") {
				t.Fatalf("bad result passed: %+v", scores)
			}
		})
	}
}

func TestNoWriteAndDraftAssertionsAreScoredOnlyWhenReviewed(t *testing.T) {
	c := Case{Reviewed: true, Expected: Expected{Reminder: &ExpectedReminder{Operations: 0}}}
	if Score(c, Result{Status: "succeeded", SimulatedReminderOperations: 1})["reminder_operations"] {
		t.Fatal("misdirected self reminder passed")
	}
	if !Score(c, Result{Status: "succeeded"})["reminder_operations"] {
		t.Fatal("no write refusal failed")
	}
	c.Reviewed = false
	if _, ok := Score(c, Result{Status: "succeeded"})["reminder_operations"]; ok {
		t.Fatal("unreviewed business expectation scored")
	}
	c.Reviewed = true
	empty := ""
	c.Expected.Reminder.Date, c.Expected.Reminder.Time = &empty, &empty
	// Nil assertions and explicit empty values cannot collapse into one another.
	raw, _ := json.Marshal(remindertool.Arguments{Action: "save", Date: "2026-10-23", Time: "15:30"})
	r := Result{Status: "succeeded", Run: ai.Run{Steps: []ai.Step{{Name: "tool_call", Output: tools.Call{Name: "set_reminder", Arguments: raw}}}}}
	if Score(c, r)["reminder_arguments"] {
		t.Fatal("invented date/time passed draft check")
	}
}
