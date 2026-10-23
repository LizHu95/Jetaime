package remindertool

import (
	"context"
	"flag"
	"os"
	"testing"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/execution"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/qa"
	"github.com/LizHu95/Jetaime/services/api/internal/reminder"
)

// Opt-in model check. The reminder service is a fake: no business data is saved
// and no reminder or chat message is delivered.
func TestReminderHistoryLive(t *testing.T) {
	configPath := os.Getenv("JETAIME_TEST_MODEL_CONFIG")
	if configPath == "" {
		t.Skip("set JETAIME_TEST_MODEL_CONFIG and the profile's API key to call a real model")
	}
	flags := llm.RegisterFlags(flag.NewFlagSet("reminder-live", flag.ContinueOnError))
	flags.File = configPath
	config, err := flags.Resolve(32768)
	if err != nil {
		t.Fatal(err)
	}
	model, err := config.New(60 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for _, draft := range []bool{false, true} {
		t.Run(map[bool]string{false: "create", true: "complete draft"}[draft], func(t *testing.T) {
			input := historyContext(t)
			if draft {
				input.ReminderDraft = &ai.ReminderDraft{ID: "draft", Version: 1, Title: "面试goodsnote", Date: "2026-10-23"}
			}
			api := &historyReminderAPI{}
			adapter := New(api, func(tools.Scope) (reminder.Operation, error) { return reminder.Operation{}, nil })
			registry, err := tools.New(qa.New(model, config.Model, config.ContextTokens, nil), adapter)
			if err != nil {
				t.Fatal(err)
			}
			service, err := execution.New(model, config.Model, config.ContextTokens, registry)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			run, err := service.Execute(ctx, ai.Invocation{Context: input, Question: "提醒我面试"})
			if err != nil {
				t.Fatal(err)
			}
			if run.Answer == nil || run.Answer.Status != "answered" {
				t.Fatalf("unexpected answer: %+v", run.Answer)
			}
			if draft {
				if api.patch.Time == nil || *api.patch.Time != "15:30" {
					t.Fatalf("historical time missing: %+v", api.patch)
				}
			} else if api.create.Date != "2026-10-23" || api.create.Time != "15:30" {
				t.Fatalf("wrong schedule: %+v", api.create)
			}
			found := false
			for _, id := range run.Answer.SourceMessageIDs {
				if id == "schedule" {
					found = true
				}
			}
			if !found {
				t.Fatalf("missing historical evidence: %v", run.Answer.SourceMessageIDs)
			}
			t.Logf("model=%s answer=%s", config.Model, run.Answer.Answer)
		})
	}
}
