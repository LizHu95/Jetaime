package extracttool

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
)

func TestRejectsForeignExtractionContext(t *testing.T) {
	input, tool, model := setup(t, `{}`)
	_, err := tool.Invoke(context.Background(), tools.Scope{Context: input, LoadExtraction: func(context.Context) (ai.Context, error) {
		input.GroupID = "other"
		return input, nil
	}}, json.RawMessage(`{"question":"整理待办"}`))
	if err == nil || model.request.Model != "" {
		t.Fatal("foreign extraction context reached the model")
	}
}
