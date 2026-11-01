package remindertool

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	mainagent "github.com/LizHu95/Jetaime/services/api/internal/ai/agent/main"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/amap"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/history"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/qa"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/recent"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/respond"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/silence"
	"github.com/LizHu95/Jetaime/services/api/internal/reminder"
)

type envelopeReminderAPI struct {
	historyReminderAPI
	creates int
}

func (s *envelopeReminderAPI) Create(ctx context.Context, scope reminder.Scope, input reminder.CreateInput, op reminder.Operation) (reminder.Result, error) {
	s.creates++
	return s.historyReminderAPI.Create(ctx, scope, input, op)
}

type envelopeReminderModel struct{ calls int }

func (m *envelopeReminderModel) Call(context.Context, llm.Request) (llm.Result, error) {
	m.calls++
	if m.calls != 1 {
		return llm.Result{}, fmt.Errorf("unexpected repair call")
	}
	return llm.Result{Content: `{"action":"save","date":"2026-11-02","sourceMessageIds":["request"],"time":"19:00","title":"理发"}`}, nil
}

func envelopeReminderContext() ai.Context {
	at := time.Date(2026, 11, 1, 23, 31, 0, 0, time.FixedZone("Asia/Shanghai", 8*3600))
	return ai.Context{GroupID: "g", ActorID: "u", Users: []ai.User{{ID: "u", Nickname: "用户"}},
		Messages: []ai.Message{{ID: "request", GroupID: "g", SenderID: "u", SentAt: at, Text: "明天19:00提醒我理发"}},
		Request:  &ai.RequestContext{MessageID: "request", SenderID: "u", SentAt: at, CurrentTime: at, Timezone: "Asia/Shanghai", TriggerMode: "proactive", AllowWrites: true}}
}

func envelopeFullCatalog(t *testing.T, model llm.ChatModel, api *envelopeReminderAPI) *tools.Registry {
	t.Helper()
	entries := []tools.Tool{respond.New(), silence.New(), qa.New(model, "test", 32768, nil), recent.New(nil),
		history.New(model, "test", 32768, nil), New(api, func(tools.Scope) (reminder.Operation, error) { return reminder.Operation{}, nil })}
	entries = append(entries, amap.New(nil, nil)...)
	registry, err := tools.New(entries...)
	if err != nil {
		t.Fatal(err)
	}
	if len(registry.Definitions()) != 15 {
		t.Fatal("production-shaped catalog changed; review inference coverage")
	}
	return registry
}

func TestTraceBareArgumentsMatchFullToolCatalog(t *testing.T) {
	api := &envelopeReminderAPI{}
	model := &envelopeReminderModel{}
	registry := envelopeFullCatalog(t, model, api)
	for _, tc := range []struct{ name, raw string }{
		{"respond", `{"status":"answered","answer":"明天晴，来源高德天气。","sourceMessageIds":[]}`},
		{"set_reminder", `{"action":"save","date":"2026-11-02","sourceMessageIds":["request"],"time":"19:00","title":"理发"}`},
		{"maps_weather", `{"city":"上海"}`},
	} {
		call, normalized, err := registry.DecodeWithEnvelope(tc.raw, registry.Definitions())
		if err != nil || !normalized || call.Name != tc.name || string(call.Arguments) != tc.raw {
			t.Fatalf("trace shape not uniquely identified: tool=%s normalized=%v err=%v", call.Name, normalized, err)
		}
	}
	// Walking/driving/cycling have the same argument shape; do not guess a mode.
	_, normalized, err := registry.DecodeWithEnvelope(`{"origin":"121.5,31.3","destination":"121.6,31.4"}`, registry.Definitions())
	if err == nil || normalized || api.creates != 0 || model.calls != 0 {
		t.Fatal("ambiguous route was inferred or decoding caused execution")
	}
}

func TestBareReminderCreatesOnceWithoutModelRepair(t *testing.T) {
	api := &envelopeReminderAPI{}
	model := &envelopeReminderModel{}
	registry := envelopeFullCatalog(t, model, api)
	agent, _ := mainagent.NewAgent(model, "test", 32768, registry)
	run, err := agent.ExecuteMain(context.Background(), envelopeReminderContext(), "设置理发提醒", nil)
	if err != nil || model.calls != 1 || api.creates != 1 || run.Answer == nil {
		t.Fatalf("envelope repair repeated inference or write: models=%d creates=%d err=%v", model.calls, api.creates, err)
	}
	if api.create.Date != "2026-11-02" || api.create.Time != "19:00" || api.create.Title != "理发" {
		t.Fatalf("normalization changed business arguments: %+v", api.create)
	}
	normalizations := 0
	for _, step := range run.Steps {
		if step.Name == "normalize_tool_call" {
			normalizations++
		}
	}
	if normalizations != 1 {
		t.Fatal("missing normalization audit")
	}
}

func TestNormalizedReminderStillChecksAuthorizationSourcesAndTime(t *testing.T) {
	for _, tc := range []struct {
		name, source, clock string
		allowWrites         bool
		code                string
	}{
		{name: "write authorization", source: "request", clock: "19:00", code: "explicit_request_required"},
		{name: "foreign evidence", allowWrites: true, source: "foreign", clock: "19:00", code: "invalid_sources"},
		{name: "invented time", allowWrites: true, source: "request", clock: "20:00", code: "time_evidence_missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := &envelopeReminderAPI{}
			adapter := New(api, func(tools.Scope) (reminder.Operation, error) { return reminder.Operation{}, nil })
			registry, err := tools.New(adapter, respond.New())
			if err != nil {
				t.Fatal(err)
			}
			raw := fmt.Sprintf(`{"action":"save","title":"理发","date":"2026-11-02","time":%q,"sourceMessageIds":[%q]}`, tc.clock, tc.source)
			call, normalized, err := registry.DecodeWithEnvelope(raw, registry.Definitions())
			if err != nil || !normalized || call.Name != "set_reminder" {
				t.Fatal("expected complete unique reminder shape", err)
			}
			input := envelopeReminderContext()
			input.Request.AllowWrites = tc.allowWrites
			_, err = registry.Execute(context.Background(), tools.Scope{Context: input}, call)
			failure, ok := tools.Repairable(err)
			if !ok || failure.Code != tc.code || api.creates != 0 {
				t.Fatalf("business gate bypassed: creates=%d err=%v", api.creates, err)
			}
		})
	}
}
