package prompts

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
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
