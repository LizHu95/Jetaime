package mainagent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/skills"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/respond"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/silence"
)

func TestToolBudgetReservesRestrictedFinalResponse(t *testing.T) {
	for _, tc := range []struct {
		name, lookup, final string
		proactive, fail     bool
		limit               int
	}{
		{name: "normal", lookup: "lookup", final: `{"name":"respond","arguments":{"status":"answered","answer":"根据查询结果回答","sourceMessageIds":[]}}`, limit: MaxToolSteps},
		{name: "mcp", lookup: "maps_text_search", final: `{"name":"respond","arguments":{"status":"answered","answer":"根据查询结果回答","sourceMessageIds":[]}}`, limit: MaxMCPToolSteps},
		{name: "proactive silence", lookup: "lookup", final: `{"name":"remain_silent","arguments":{"reason":"无需补充"}}`, proactive: true, limit: MaxToolSteps},
		{name: "block another lookup", lookup: "lookup", final: `{"name":"lookup","arguments":{}}`, fail: true, limit: MaxToolSteps},
		{name: "block write", lookup: "lookup", final: `{"name":"write","arguments":{}}`, fail: true, limit: MaxToolSteps},
		{name: "reject invented sources", lookup: "lookup", final: `{"name":"respond","arguments":{"status":"answered","answer":"编造来源","sourceMessageIds":["foreign"]}}`, fail: true, limit: MaxToolSteps},
		{name: "reject silence for explicit request", lookup: "lookup", final: `{"name":"remain_silent","arguments":{"reason":"忽略请求"}}`, fail: true, limit: MaxToolSteps},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reads, writes := 0, 0
			lookup := tools.NewTyped(tools.Metadata{Name: tc.lookup, Description: "查询", ReadOnly: true}, func(context.Context, tools.Scope, struct{}) (budgetLookupOutput, error) {
				reads++
				return budgetLookupOutput{Value: "已有证据"}, nil
			})
			write := tools.NewTyped(tools.Metadata{Name: "write", Description: "写入"}, func(context.Context, tools.Scope, struct{}) (struct{}, error) { writes++; return struct{}{}, nil })
			registry, err := tools.New(lookup, write, respond.New(), silence.New())
			if err != nil {
				t.Fatal(err)
			}
			model := &proactiveModel{}
			for i := 0; i < tc.limit; i++ {
				model.outputs = append(model.outputs, `{"name":"`+tc.lookup+`","arguments":{}}`)
			}
			model.outputs = append(model.outputs, tc.final)
			agent, err := NewAgent(model, "test", 32768, registry)
			if err != nil {
				t.Fatal(err)
			}
			input := testContext()
			if tc.proactive {
				ref, _ := skills.Route("pacing_conflict")
				input.Request = &ai.RequestContext{Skill: &ref, MessageID: "m6", SenderID: input.ActorID, SentAt: input.Messages[1].SentAt, CurrentTime: time.Now(), Timezone: "Asia/Shanghai", TriggerMode: "proactive"}
			}
			run, err := agent.ExecuteMain(context.Background(), input, "查询后回复", nil)
			if tc.fail {
				if !errors.Is(err, ai.ErrInvalidDecision) || run.Status != "failed" {
					t.Fatalf("expected validation failure: %+v %v", run, err)
				}
			} else if err != nil || run.Status != "succeeded" {
				t.Fatalf("final response failed: %+v %v", run, err)
			}
			if reads != tc.limit || writes != 0 || len(model.requests) != tc.limit+1 {
				t.Fatalf("budget bypassed: reads=%d writes=%d rounds=%d", reads, writes, len(model.requests))
			}
			last := model.requests[tc.limit]
			var payload struct {
				Tools []tools.Definition `json:"tools"`
			}
			if err := json.Unmarshal([]byte(last.Messages[1].Content), &payload); err != nil {
				t.Fatal(err)
			}
			for _, d := range payload.Tools {
				if d.Name != "respond" && !(tc.proactive && d.Name == "remain_silent") {
					t.Fatalf("unsafe final catalog: %s", d.Name)
				}
			}
			if !strings.Contains(string(last.Format), `"respond"`) || strings.Contains(string(last.Format), `"write"`) {
				t.Fatal("final schema not restricted")
			}
			if !strings.Contains(last.Messages[0].Content, "工具执行额度已用完") || !strings.Contains(model.requests[tc.limit-1].Messages[0].Content, "最后一轮") {
				t.Fatal("missing budget instructions")
			}
			if !strings.Contains(last.Messages[2].Content, "已有证据") {
				t.Fatal("final response lost evidence")
			}
			for _, r := range model.requests {
				if tc.proactive {
					body, err := skills.Load(*input.Request.Skill)
					if err != nil || !strings.Contains(r.Messages[0].Content, body) {
						t.Fatal("skill lost during tool loop or final response", err)
					}
				}
				if err := llm.CheckContextBudget(r, 32768); err != nil {
					t.Fatal(err)
				}
			}
			if !tc.fail && tc.proactive && !run.Silent {
				t.Fatal("proactive completion did not abstain")
			}
		})
	}
}
