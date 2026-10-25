package mainagent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/agent/main/prompts"
	aicontext "github.com/LizHu95/Jetaime/services/api/internal/ai/context"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/skills"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/silence"
)

// Production replay: the first request fits, but feedback exceeds round two's budget.
func TestMainRebudgetsAfterToolFeedback(t *testing.T) {
	for _, rejected := range []bool{true, false} {
		name := "result"
		if rejected {
			name = "repair"
		}
		t.Run(name, func(t *testing.T) {
			input := testContext()
			background := input.Messages[0]
			background.ID = "optional"
			background.Text = strings.Repeat("optional-background", 200)
			input.Messages = append([]ai.Message{background}, input.Messages...)
			ref, _ := skills.Route("pacing_conflict")
			input.Request = &ai.RequestContext{Skill: &ref, MessageID: "m6", SenderID: input.ActorID, SentAt: input.Messages[2].SentAt, CurrentTime: time.Now(), Timezone: "Asia/Shanghai", TriggerMode: "proactive", RelatedMessageIDs: []string{"m1"}}
			lookup := tools.NewTyped(tools.Metadata{Name: "maps_around_search", Description: "查询周边", ReadOnly: true}, func(context.Context, tools.Scope, budgetLookupInput) (budgetLookupOutput, error) {
				if rejected {
					return budgetLookupOutput{}, tools.InvalidArguments("invalid_amap_arguments", strings.Repeat("位置不可用，请提供地点。", 150))
				}
				return budgetLookupOutput{Value: strings.Repeat("bounded-result", 400)}, nil
			})
			registry, err := tools.New(lookup, silence.New())
			if err != nil {
				t.Fatal(err)
			}
			model := &proactiveModel{outputs: []string{`{"name":"maps_around_search","arguments":{"location":"current"}}`, `{"name":"remain_silent","arguments":{"reason":"定位不足，不重复查询"}}`}}
			selected, err := aicontext.ExecutionContext(input)
			if err != nil {
				t.Fatal(err)
			}
			req, err := prompts.MainRequest("test", selected, "附近去哪玩", 65536, registry.Definitions()...)
			if err != nil {
				t.Fatal(err)
			}
			capacity := llm.InputTokenUpperBound(req) + req.Options.NumPredict + ToolFeedbackReserve + 32
			agent, err := NewAgent(model, "test", capacity)
			if err != nil {
				t.Fatal(err)
			}
			agent.Tools = registry
			run, err := agent.ExecuteMain(context.Background(), input, "附近去哪玩", nil)
			if err != nil || !run.Silent || len(model.requests) != 2 {
				t.Fatalf("repair failed: silent=%t requests=%d err=%v", run.Silent, len(model.requests), err)
			}
			for _, request := range model.requests {
				body, err := skills.Load(ref)
				if err != nil || !strings.Contains(request.Messages[0].Content, body) {
					t.Fatal("compaction or repair discarded the selected skill", err)
				}
				if err := llm.CheckContextBudget(request, capacity); err != nil {
					t.Fatal("over-budget provider call", err)
				}
			}
			second := model.requests[1]
			if len(second.Messages) != 3 {
				t.Fatal("tool feedback disappeared during compaction")
			}
			feedback := second.Messages[2].Content
			if rejected && !strings.Contains(feedback, "invalid_amap_arguments") {
				t.Fatal("repair feedback lost")
			}
			if !rejected && !strings.Contains(feedback, "bounded-result") {
				t.Fatal("tool evidence lost")
			}
			var payload struct {
				Context ai.Context `json:"context"`
			}
			if err = json.Unmarshal([]byte(second.Messages[1].Content), &payload); err != nil {
				t.Fatal(err)
			}
			ids := map[string]bool{}
			for _, message := range payload.Context.Messages {
				ids[message.ID] = true
			}
			if ids["optional"] || !ids["m1"] || !ids["m6"] || payload.Context.HistoryScope == nil || !payload.Context.HistoryScope.EarlierMessagesOmitted {
				t.Fatal("compaction removed anchors or retained optional background")
			}
			trimmed := false
			for _, step := range run.Steps {
				if step.Name == "context_trim" {
					trimmed = true
				}
			}
			if !trimmed {
				t.Fatal("missing budget compaction diagnostics")
			}
		})
	}
}

func TestSkillBudgetCannotDropStrategyOrPinnedEvidence(t *testing.T) {
	input := testContext()
	input.Request = &ai.RequestContext{MessageID: "m6", SenderID: input.ActorID, TriggerMode: "proactive", RelatedMessageIDs: []string{"m1"}}
	registry, err := tools.New(silence.New())
	if err != nil {
		t.Fatal(err)
	}
	base, err := prompts.MainRequest("test", input, "协调暂停", 65536, registry.Definitions()...)
	if err != nil {
		t.Fatal(err)
	}
	capacity := llm.InputTokenUpperBound(base) + base.Options.NumPredict + 16
	ref, _ := skills.Route("pacing_conflict")
	input.Request.Skill = &ref
	model := &proactiveModel{}
	agent, err := NewAgent(model, "test", capacity)
	if err != nil {
		t.Fatal(err)
	}
	agent.Tools = registry
	run, err := agent.ExecuteMain(context.Background(), input, "协调暂停", nil)
	if !errors.Is(err, ai.ErrContext) || len(model.requests) != 0 || run.Status != "failed" {
		t.Fatalf("over-budget strategy reached provider: calls=%d err=%v", len(model.requests), err)
	}
}

type budgetLookupInput struct {
	Location string `json:"location"`
}
type budgetLookupOutput struct {
	Value string `json:"value"`
}

func TestMainFeedbackCannotDiscardRequiredEvidence(t *testing.T) {
	input := testContext()
	input.Request = &ai.RequestContext{MessageID: "m6", SenderID: input.ActorID, SentAt: input.Messages[1].SentAt, CurrentTime: time.Now(), Timezone: "Asia/Shanghai", TriggerMode: "proactive", RelatedMessageIDs: []string{"m1"}}
	lookup := tools.NewTyped(tools.Metadata{Name: "lookup", Description: "读取证据", ReadOnly: true}, func(context.Context, tools.Scope, budgetLookupInput) (budgetLookupOutput, error) {
		return budgetLookupOutput{Value: strings.Repeat("large-result", 2000)}, nil
	})
	registry, err := tools.New(lookup, silence.New())
	if err != nil {
		t.Fatal(err)
	}
	model := &proactiveModel{outputs: []string{`{"name":"lookup","arguments":{"location":"current"}}`}}
	selected, err := aicontext.ExecutionContext(input)
	if err != nil {
		t.Fatal(err)
	}
	req, err := prompts.MainRequest("test", selected, "核实", 65536, registry.Definitions()...)
	if err != nil {
		t.Fatal(err)
	}
	capacity := llm.InputTokenUpperBound(req) + req.Options.NumPredict + 32
	agent, err := NewAgent(model, "test", capacity)
	if err != nil {
		t.Fatal(err)
	}
	agent.Tools = registry
	_, err = agent.ExecuteMain(context.Background(), input, "核实", nil)
	if !errors.Is(err, ai.ErrContext) || len(model.requests) != 1 {
		t.Fatal("required evidence or hard model limit was bypassed", err)
	}
}
