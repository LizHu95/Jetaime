package tools

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/telemetry"
	"github.com/google/jsonschema-go/jsonschema"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

type contractInput struct {
	Name  string `json:"name" jsonschema:"名称"`
	Mode  string `json:"mode"`
	Count int    `json:"count"`
	Note  string `json:"note,omitempty"`
}
type contractOutput struct {
	Status string `json:"status"`
	Count  int    `json:"count"`
}

func contractOptions() Options[contractOutput] {
	return Options[contractOutput]{Input: func(s *jsonschema.Schema) {
		s.Properties["name"].MinLength = jsonschema.Ptr(1)
		s.Properties["mode"].Enum = []any{"save", "cancel"}
		s.Properties["count"].Minimum = jsonschema.Ptr(float64(1))
	}, Output: func(s *jsonschema.Schema) { s.Properties["status"].Enum = []any{"saved"} }, Present: func(contractOutput) Completion {
		return Completion{Answer: &ai.Answer{Status: "answered", Answer: "完成", SourceMessageIDs: []string{}}}
	}}
}
func contractScope() Scope {
	return Scope{Context: ai.Context{GroupID: "g", ActorID: "u", Users: []ai.User{{ID: "u", Nickname: "User"}}, Messages: []ai.Message{{ID: "m", GroupID: "g", SenderID: "u", Text: "测试", SentAt: time.Now()}}}}
}
func contractMetadata() Metadata {
	return Metadata{Name: "save_item", Description: "保存项目", Terminal: true, Idempotent: true}
}
func TestTypedContractValidatesBeforeHandler(t *testing.T) {
	calls := 0
	tool := NewTyped(contractMetadata(), func(_ context.Context, s Scope, in contractInput) (contractOutput, error) {
		calls++
		if s.Context.ActorID != "u" || s.Context.GroupID != "g" {
			t.Fatal("trusted scope changed")
		}
		return contractOutput{Status: "saved", Count: in.Count}, nil
	}, contractOptions())
	registry, err := New(tool)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		`not json`, `null`, `[]`,
		`{"name":"x","mode":"save"}`,
		`{"name":"x","mode":"unknown","count":1}`,
		`{"name":"","mode":"save","count":1}`,
		`{"name":"x","mode":"save","count":0}`,
		`{"name":"x","mode":"save","count":"1"}`,
		`{"name":"x","mode":"save","count":1.5}`,
		`{"name":"x","mode":"save","count":1,"groupId":"other"}`,
	} {
		result, err := registry.Execute(context.Background(), contractScope(), Call{Name: "save_item", Arguments: json.RawMessage(raw)})
		if _, ok := Repairable(err); !ok || calls != 0 || len(result.Data) != 0 || result.Completion.Answer != nil {
			t.Fatalf("bad arguments caused effects: %s calls=%d err=%v", raw, calls, err)
		}
	}
	result, err := registry.Execute(context.Background(), contractScope(), Call{Name: "save_item", Arguments: json.RawMessage(`{"name":"x","mode":"save","count":2}`)})
	if err != nil || calls != 1 || !result.Terminal || result.Completion.Answer == nil || result.Execution.StartedAt.IsZero() {
		t.Fatalf("valid invocation failed: %+v %v", result, err)
	}
	var out contractOutput
	if err := json.Unmarshal(result.Data, &out); err != nil || out.Count != 2 {
		t.Fatal("invalid structured output", err)
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(tool.Definition().InputSchema, &schema); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(schema.Required, []string{"name", "mode", "count"}) || schema.Properties["note"] == nil || schema.Properties["actorId"] != nil {
		t.Fatalf("type/schema mismatch: %+v", schema)
	}
	d := tool.Definition()
	if len(d.OutputSchema) == 0 || !d.Annotations.Idempotent {
		t.Fatal("missing output contract or execution hints")
	}
	d.InputSchema[0] = '!'
	if !json.Valid(tool.Definition().InputSchema) {
		t.Fatal("catalog can mutate executor schema")
	}
}
func TestInvalidOutputIsNotPublishedOrRepairable(t *testing.T) {
	calls, presented := 0, 0
	options := contractOptions()
	options.Present = func(contractOutput) Completion { presented++; return Completion{} }
	tool := NewTyped(contractMetadata(), func(context.Context, Scope, contractInput) (contractOutput, error) {
		calls++
		return contractOutput{Status: "unexpected"}, nil
	}, options)
	out, err := tool.Invoke(context.Background(), contractScope(), json.RawMessage(`{"name":"x","mode":"save","count":1}`))
	if !errors.Is(err, ErrInvalidOutput) || calls != 1 || presented != 0 || len(out.Data) != 0 || out.Terminal {
		t.Fatalf("invalid output escaped: %+v %v", out, err)
	}
	if _, ok := Repairable(err); ok {
		t.Fatal("a possibly committed write would be replayed")
	}
}
func TestBadContractsFailRegistration(t *testing.T) {
	handler := func(context.Context, Scope, contractInput) (contractOutput, error) { return contractOutput{}, nil }
	var nilTool *Typed[contractInput, contractOutput]
	for _, tool := range []Tool{
		nil, nilTool,
		NewTyped(Metadata{Name: "bad name", Description: "test"}, handler),
		NewTyped(contractMetadata(), Handler[contractInput, contractOutput](nil)),
		NewTyped(contractMetadata(), handler, Options[contractOutput]{Input: func(s *jsonschema.Schema) { s.Properties["name"].Pattern = "[" }}),
		NewTyped(contractMetadata(), func(context.Context, Scope, struct{}) (string, error) { return "", nil }),
		NewTyped(contractMetadata(), func(context.Context, Scope, struct{}) (struct{ Unsupported chan int }, error) {
			return struct{ Unsupported chan int }{}, nil
		}),
	} {
		if _, err := New(tool); err == nil {
			t.Fatal("invalid contract registered")
		}
	}
	tool := NewTyped(contractMetadata(), handler)
	if _, err := New(tool, tool); err == nil {
		t.Fatal("duplicate tool registered")
	}
}
func TestTimeoutCancellationAndErrorsPreserveExecutionBoundary(t *testing.T) {
	scope := contractScope()
	args := json.RawMessage(`{"name":"x","mode":"save","count":1}`)
	calls := 0
	meta := contractMetadata()
	meta.Timeout = 5 * time.Millisecond
	tool := NewTyped(meta, func(ctx context.Context, _ Scope, _ contractInput) (contractOutput, error) {
		calls++
		RecordStep(ctx, ai.Step{Name: "work", Status: "running"})
		<-ctx.Done()
		return contractOutput{}, ctx.Err()
	})
	out, err := tool.Invoke(context.Background(), scope, args)
	if !errors.Is(err, context.DeadlineExceeded) || calls != 1 || len(out.Execution.Steps) != 1 || len(out.Data) != 0 {
		t.Fatalf("timeout record lost: %+v %v", out, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = tool.Invoke(ctx, scope, args); !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatal("cancelled invocation executed", err)
	}
	tool = NewTyped(contractMetadata(), func(context.Context, Scope, contractInput) (contractOutput, error) {
		calls++
		return contractOutput{}, errors.New("storage unavailable")
	})
	out, err = tool.Invoke(context.Background(), scope, args)
	if err == nil || !strings.Contains(err.Error(), "storage unavailable") || out.Terminal || len(out.Data) > 0 {
		t.Fatal("infrastructure failure treated as success")
	}
	if _, ok := Repairable(err); ok {
		t.Fatal("infrastructure failure treated as argument repair")
	}
}

func TestToolDiagnosticStepsExportWithParentAndMeasuredTime(t *testing.T) {
	old := otel.GetTracerProvider()
	t.Cleanup(func() { otel.SetTracerProvider(old) })
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	otel.SetTracerProvider(provider)
	defer func() { _ = provider.Shutdown(context.Background()) }()
	t.Setenv("JETAIME_TRACE_CAPTURE_CONTENT", "false")
	started := time.Now().Add(-time.Second)
	tool := NewTyped(contractMetadata(), func(ctx context.Context, _ Scope, in contractInput) (contractOutput, error) {
		RecordStep(ctx, ai.Step{Name: "build_context", Status: "failed", StartedAt: started, DurationNS: (7 * time.Millisecond).Nanoseconds(), Error: "private-provider-body", Output: "private-chat"})
		return contractOutput{Status: "saved", Count: in.Count}, nil
	}, contractOptions())
	ctx, root := telemetry.Start(context.Background(), "worker", "CHAIN")
	_, err := tool.Invoke(ctx, contractScope(), json.RawMessage(`{"name":"test","mode":"save","count":1}`))
	telemetry.End(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	spans := exporter.GetSpans()
	var toolID trace.SpanID
	for _, span := range spans {
		if span.Name == "ai.tool.execute" {
			toolID = span.SpanContext.SpanID()
		}
	}
	found := false
	for _, span := range spans {
		if span.Name != "step.build_context" {
			continue
		}
		found = true
		if span.Parent.SpanID() != toolID || span.SpanContext.TraceID() != root.SpanContext().TraceID() {
			t.Fatal("step disconnected from tool trace")
		}
		if !span.StartTime.Equal(started) || span.EndTime.Sub(span.StartTime) != 7*time.Millisecond || span.Status.Code != codes.Error {
			t.Fatal("step timing or failure lost")
		}
		for _, attr := range span.Attributes {
			if strings.Contains(attr.Value.AsString(), "private-") {
				t.Fatal("private diagnostics exported without opt-in")
			}
		}
		if strings.Contains(span.Status.Description, "private-") {
			t.Fatal("raw error exported")
		}
	}
	if !found {
		t.Fatal("missing context step span")
	}
}
