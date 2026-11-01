package replay

import (
	"encoding/json"
	"strings"

	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
	remindertool "github.com/LizHu95/Jetaime/services/api/internal/ai/tools/reminder"
)

func scoreReminder(expected ExpectedReminder, r Result, scores map[string]bool) {
	scores["reminder_operations"] = r.SimulatedReminderOperations == expected.Operations
	var calls []remindertool.Arguments
	var results []remindertool.Output
	for _, step := range r.Run.Steps {
		if step.Name == "tool_call" {
			raw, _ := json.Marshal(step.Output)
			var call tools.Call
			if json.Unmarshal(raw, &call) == nil && call.Name == "set_reminder" {
				var args remindertool.Arguments
				if json.Unmarshal(call.Arguments, &args) == nil {
					calls = append(calls, args)
				}
			}
		}
		if step.Name == "tool_result" && step.Status == "succeeded" {
			raw, _ := json.Marshal(step.Input)
			var call tools.Call
			if json.Unmarshal(raw, &call) == nil && call.Name == "set_reminder" {
				raw, _ = json.Marshal(step.Output)
				var result remindertool.Output
				if json.Unmarshal(raw, &result) == nil {
					results = append(results, result)
				}
			}
		}
	}
	if expected.Action != "" || len(expected.TitleContainsAll) > 0 || expected.Date != nil || expected.Time != nil {
		// Inspect the last submitted arguments: repaired bad attempts are retained
		// in the trace, while duplicate committed writes fail the operations check.
		ok := len(calls) > 0
		if ok {
			args := calls[len(calls)-1]
			ok = (expected.Action == "" || args.Action == expected.Action) && (expected.Date == nil || args.Date == *expected.Date) && (expected.Time == nil || args.Time == *expected.Time)
			for _, term := range expected.TitleContainsAll {
				ok = ok && strings.Contains(strings.ToLower(args.Title), strings.ToLower(term))
			}
		}
		scores["reminder_arguments"] = ok
	}
	if expected.ResultStatus != "" || expected.ReminderStatus != "" || expected.UserID != "" || len(expected.SourceMessageIDs) > 0 {
		ok := len(results) == 1
		if ok {
			result := results[0].Result
			// The tool adds the current request to validated sources. Score that
			// normalized provenance instead of rejecting an equivalent model call.
			for _, id := range expected.SourceMessageIDs {
				found := false
				for _, cited := range results[0].SourceMessageIDs {
					found = found || cited == id
				}
				ok = ok && found
			}
			ok = ok && (expected.ResultStatus == "" || result.Status == expected.ResultStatus)
			if expected.ReminderStatus != "" || expected.UserID != "" {
				ok = ok && result.Reminder != nil
				if result.Reminder != nil {
					ok = ok && (expected.ReminderStatus == "" || result.Reminder.Status == expected.ReminderStatus) && (expected.UserID == "" || result.Reminder.UserID == expected.UserID)
				}
			}
			if expected.Date != nil || expected.Time != nil || len(expected.TitleContainsAll) > 0 {
				ok = ok && result.Reminder != nil
				if result.Reminder != nil {
					ok = ok && (expected.Date == nil || result.Reminder.Date == *expected.Date) && (expected.Time == nil || result.Reminder.Time == *expected.Time)
					for _, term := range expected.TitleContainsAll {
						ok = ok && strings.Contains(strings.ToLower(result.Reminder.Title), strings.ToLower(term))
					}
				}
			}
		}
		scores["reminder_result"] = ok
	}
}
