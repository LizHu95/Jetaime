package prompts

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/skills"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
)

func TestMainRequestOmitsBackendContractsAndDuplicateSystemSchema(t *testing.T) {
	definition := tools.Definition{Name: "lookup", Description: "读取历史", InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"],"additionalProperties":false}`), OutputSchema: json.RawMessage(`{"description":"` + strings.Repeat("large-backend-output", 10000) + `"}`), Annotations: tools.Annotations{ReadOnly: true, Idempotent: true}}
	req, err := MainRequest("test", ai.Context{}, "上周聊了啥", 8192, definition)
	if err != nil {
		t.Fatal(err)
	}
	if err = llm.CheckContextBudget(req, 8192); err != nil {
		t.Fatal("unused output schema inflated routing request", err)
	}
	if req.Messages[0].Content != Main {
		t.Fatal("response schema repeated in system prompt")
	}
	var payload struct {
		Tools []map[string]json.RawMessage `json:"tools"`
	}
	if err = json.Unmarshal([]byte(req.Messages[1].Content), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Tools) != 1 || len(payload.Tools[0]) != 3 || payload.Tools[0]["inputSchema"] == nil || payload.Tools[0]["outputSchema"] != nil || payload.Tools[0]["annotations"] != nil {
		t.Fatal("model catalog contains execution-only metadata")
	}
	if !strings.Contains(string(req.Format), `"lookup"`) || !strings.Contains(string(req.Format), `"query"`) {
		t.Fatal("response format lost callable tool constraints")
	}
	if !strings.Contains(string(definition.OutputSchema), "large-backend-output") {
		t.Fatal("backend output contract changed")
	}
}

func TestMainRequestLoadsOnlyTheSelectedCommunicationSkill(t *testing.T) {
	for _, breakdown := range []string{"understanding_mismatch", "support_mismatch", "communication_escalation", "pacing_conflict", "repair_blocked"} {
		ref, _ := skills.Route(breakdown)
		body, err := skills.Load(ref)
		if err != nil {
			t.Fatal(err)
		}
		input := ai.Context{Request: &ai.RequestContext{TriggerMode: "proactive", Skill: &ref}}
		req, err := MainRequest("test", input, "处理当前沟通", 32768)
		if err != nil {
			t.Fatal(err)
		}
		system := req.Messages[0].Content
		if !strings.HasPrefix(system, Main) || !strings.Contains(system, body) || strings.Count(system, "Skill ID: ") != 1 || !strings.Contains(system, "Skill Version: "+ref.Version) {
			t.Fatal("lost base constraints or selected strategy")
		}
		if strings.Contains(req.Messages[1].Content, ref.ID) || strings.Contains(req.Messages[1].Content, body) {
			t.Fatal("strategy included in conversation payload")
		}
		without := input
		without.Request = &ai.RequestContext{TriggerMode: "proactive"}
		base, err := MainRequest("test", without, "处理当前沟通", 32768)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(base.Messages[0].Content, "Skill ID:") || llm.InputTokenUpperBound(req) <= llm.InputTokenUpperBound(base)+len(body) {
			t.Fatal("strategy missing from full request budget")
		}
	}
}

func TestMainRequestRejectsSkillOutsideProactiveReadOnlyExecution(t *testing.T) {
	ref, _ := skills.Route("pacing_conflict")
	for _, request := range []ai.RequestContext{
		{TriggerMode: "mention", Skill: &ref},
		{TriggerMode: "proactive", AllowWrites: true, Skill: &ref},
		{TriggerMode: "proactive", Skill: &ai.SkillRef{ID: "custom", Version: "v1", Breakdown: "pacing_conflict"}},
	} {
		if _, err := MainRequest("test", ai.Context{Request: &request}, "处理当前目标", 32768); !errors.Is(err, ai.ErrInvalidDecision) {
			t.Fatal("invalid skill execution accepted", err)
		}
	}
	var request ai.RequestContext
	if err := json.Unmarshal([]byte(`{"triggerMode":"proactive","Skill":{"id":"custom","version":"v1","breakdown":"pacing_conflict"}}`), &request); err != nil || request.Skill != nil {
		t.Fatal("conversation data injected a server skill", err)
	}
}
