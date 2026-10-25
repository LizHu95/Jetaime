package execution

import (
	"context"
	"testing"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	mainagent "github.com/LizHu95/Jetaime/services/api/internal/ai/agent/main"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/respond"
	"github.com/LizHu95/Jetaime/services/api/internal/telemetry"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

type skillTraceModel struct{}

func (skillTraceModel) Call(ctx context.Context, _ llm.Request) (llm.Result, error) {
	_, span := telemetry.Start(ctx, "test.model", "LLM")
	telemetry.End(span, nil)
	return llm.Result{Content: `{"name":"respond","arguments":{"status":"answered","answer":"可以先听完对方的表达。","sourceMessageIds":["m2"]}}`}, nil
}

func TestSkillVersionTracedOnMainModelAndToolWithoutContentCapture(t *testing.T) {
	previous := otel.GetTracerProvider()
	t.Cleanup(func() { otel.SetTracerProvider(previous) })
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	otel.SetTracerProvider(provider)
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	t.Setenv("JETAIME_TRACE_CAPTURE_CONTENT", "false")
	agent, err := mainagent.NewAgent(skillTraceModel{}, "test", 32768)
	if err != nil {
		t.Fatal(err)
	}
	agent.Tools, err = tools.New(respond.New())
	if err != nil {
		t.Fatal(err)
	}
	judge := &testRecognizer{result: ai.RecognitionResult{Decision: ai.Recognition{ShouldIntervene: true, Reason: "支持需要不匹配", Breakdowns: []ai.Breakdown{{Type: "support_mismatch", EvidenceMessageIDs: []string{"m1", "m2"}}}, Intent: &ai.InterventionIntent{Type: "suggest", Objective: "接住已明确的倾听需要", EvidenceMessageIDs: []string{"m1", "m2"}}}}}
	entry, err := newEntry(agent, judge)
	if err != nil {
		t.Fatal(err)
	}
	run, err := entry.Invoke(context.Background(), passiveInput())
	if err != nil || run.Skill == nil || run.Skill.ID != "clarify-support" {
		t.Fatal("selected skill not recorded", run.Skill, err)
	}
	seen := map[string]bool{}
	for _, span := range exporter.GetSpans() {
		switch span.Name {
		case "ai.skill.execute", "assistant.main", "test.model", "ai.tool.execute":
		default:
			continue
		}
		attrs := map[string]attribute.Value{}
		for _, attr := range span.Attributes {
			attrs[string(attr.Key)] = attr.Value
		}
		if attrs["skill.id"].AsString() != "clarify-support" || attrs["skill.version"].AsString() != run.Skill.Version || attrs["skill.breakdown"].AsString() != "support_mismatch" || attrs["skill.selection_reason"].AsString() != "primary_breakdown" {
			t.Fatalf("skill identity lost on %s: %v", span.Name, attrs)
		}
		if _, ok := attrs["input.value"]; ok {
			t.Fatal("skill tracing enabled private content capture")
		}
		seen[span.Name] = true
	}
	if len(seen) != 4 {
		t.Fatal("missing traced execution stages", seen)
	}
}
