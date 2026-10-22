package mainagent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/agent/main/prompts"
	aicontext "github.com/LizHu95/Jetaime/services/api/internal/ai/context"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/silence"
)

func thirtyMessageContext() ai.Context {
	input := testContext()
	seed := input.Messages[0]
	input.Messages = nil
	for i := 0; i < 30; i++ {
		message := seed
		message.ID = fmt.Sprintf("m%d", i)
		message.SentAt = seed.SentAt.Add(time.Duration(i) * time.Second)
		message.Text = strings.Repeat("background", 20)
		input.Messages = append(input.Messages, message)
	}
	input.Request = &ai.RequestContext{MessageID: "m29", SenderID: input.ActorID, SentAt: input.Messages[29].SentAt, CurrentTime: time.Now(), Timezone: "Asia/Shanghai", TriggerMode: "proactive"}
	return input
}

func TestProactiveRecognizesThirtyButExecutesWithEvidenceAndSixRecent(t *testing.T) {
	model := &proactiveModel{outputs: []string{
		`{"topicContext":{"topic":"当前测试事项","continuity":"continuation","messageIds":["m0","m1","m2","m3","m4","m5","m6","m7","m8","m9","m10","m11","m12","m13","m14","m15","m16","m17","m18","m19","m20","m21","m22","m23","m24","m25","m26","m27","m28","m29"],"anchorMessageIds":["m29"]},"action":"intervene","verification":null,"reason":"需要补充地点","breakdowns":[{"type":"underspecified_plan","evidenceMessageIds":["m2"]}],"intent":{"type":"clarify","objective":"确认地点","evidenceMessageIds":["m0"]}}`,
		`{"name":"remain_silent","arguments":{"reason":"先等待成员补充"}}`,
	}}
	agent, err := NewAgent(model, "test", 32768)
	if err != nil {
		t.Fatal(err)
	}
	agent.Tools, err = tools.New(silence.New())
	if err != nil {
		t.Fatal(err)
	}
	input := thirtyMessageContext()
	run, err := invokePassiveEntry(t, agent, input)
	if err != nil || !run.Silent || len(model.requests) != 2 {
		t.Fatal("proactive execution failed", err)
	}
	var recognition ai.RecognitionInput
	if err = json.Unmarshal([]byte(model.requests[0].Messages[1].Content), &recognition); err != nil || len(recognition.Messages) != 30 {
		t.Fatal("recognition lost its thirty-message window", err)
	}
	var execution struct {
		Context ai.Context `json:"context"`
	}
	if err = json.Unmarshal([]byte(model.requests[1].Messages[1].Content), &execution); err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, message := range execution.Context.Messages {
		ids[message.ID] = true
	}
	if len(ids) != 9 || !ids["m0"] || !ids["m2"] || !ids["m29"] || ids["m22"] || !ids["m23"] {
		t.Fatal("execution did not retain exactly its evidence and six recent originals")
	}
	if len(input.Messages) != 30 || len(input.Request.RelatedMessageIDs) != 0 {
		t.Fatal("recognition snapshot mutated")
	}
}

func TestMainReservesToolSpaceBeforeOptionalBackground(t *testing.T) {
	for _, mcp := range []bool{false, true} {
		t.Run(fmt.Sprint("mcp=", mcp), func(t *testing.T) {
			name, reserve := "lookup", ToolFeedbackReserve
			if mcp {
				name, reserve = "maps_text_search", MCPFeedbackReserve
			}
			lookup := tools.NewTyped(tools.Metadata{Name: name, Description: strings.Repeat("tool description ", 100), ReadOnly: true}, func(context.Context, tools.Scope, budgetLookupInput) (budgetLookupOutput, error) {
				t.Fatal("unexpected tool call")
				return budgetLookupOutput{}, nil
			})
			registry, err := tools.New(lookup, silence.New())
			if err != nil {
				t.Fatal(err)
			}
			input := thirtyMessageContext()
			input.Request.RelatedMessageIDs = []string{"m0"}
			required := input
			required.Messages = []ai.Message{input.Messages[0], input.Messages[29]}
			required, err = aicontext.ExecutionContext(required)
			if err != nil {
				t.Fatal(err)
			}
			req, err := prompts.MainRequest("test", required, "核实", 65536, registry.Definitions()...)
			if err != nil {
				t.Fatal(err)
			}
			capacity := llm.InputTokenUpperBound(req) + req.Options.NumPredict + reserve + 700
			model := &proactiveModel{outputs: []string{`{"name":"remain_silent","arguments":{"reason":"暂不发言"}}`}}
			agent, err := NewAgent(model, "test", capacity)
			if err != nil {
				t.Fatal(err)
			}
			agent.Tools = registry
			run, err := agent.ExecuteMain(context.Background(), input, "核实", nil)
			if err != nil || !run.Silent || len(model.requests) != 1 {
				t.Fatal("assembly failed", err)
			}
			actual := model.requests[0]
			if llm.InputTokenUpperBound(actual)+actual.Options.NumPredict+reserve > capacity {
				t.Fatal("optional background consumed the tool reserve")
			}
			var payload struct {
				Context ai.Context `json:"context"`
			}
			if err = json.Unmarshal([]byte(actual.Messages[1].Content), &payload); err != nil {
				t.Fatal(err)
			}
			if len(payload.Context.Messages) >= aicontext.DefaultRecentMessages+2 || payload.Context.Messages[0].ID != "m0" || payload.Context.Messages[len(payload.Context.Messages)-1].ID != "m29" {
				t.Fatal("tool definitions or mandatory evidence lost priority")
			}
		})
	}
}
