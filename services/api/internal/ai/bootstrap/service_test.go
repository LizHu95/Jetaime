package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/qa"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/respond"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/silence"
)

type disabledExtractionModel struct {
	calls int
}

func (m *disabledExtractionModel) Call(_ context.Context, request llm.Request) (llm.Result, error) {
	m.calls++
	var payload struct {
		Tools []tools.Definition `json:"tools"`
	}
	if err := json.Unmarshal([]byte(request.Messages[1].Content), &payload); err != nil {
		return llm.Result{}, err
	}
	if len(payload.Tools) != 1 || payload.Tools[0].Name != "answer_question" {
		return llm.Result{}, errors.New("unexpected explicit tool catalog")
	}
	return llm.Result{Content: `{"name":"extract_todos","arguments":{"question":"整理待办"}}`}, nil
}

func TestExplicitQAToolsRejectExtraction(t *testing.T) {
	model := &disabledExtractionModel{}
	registry, err := tools.New(qa.New(model, "test", 16384, nil))
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(model, "test", 16384, registry)
	if err != nil {
		t.Fatal(err)
	}
	input := ai.Context{GroupID: "g", ActorID: "u", Users: []ai.User{{ID: "u", Nickname: "用户"}}, Messages: []ai.Message{{ID: "m", GroupID: "g", SenderID: "u", Text: "整理待办", SentAt: time.Now()}}}
	_, err = service.Execute(context.Background(), ai.Invocation{Context: input, Question: "整理待办", LoadExtraction: func(context.Context) (ai.Context, error) {
		t.Fatal("disabled tool loaded extraction context")
		return input, nil
	}})
	if !errors.Is(err, ai.ErrInvalidDecision) || model.calls != 3 {
		t.Fatalf("disabled extraction executed: calls=%d err=%v", model.calls, err)
	}
}

type customCatalogModel struct{ t *testing.T }

func (m customCatalogModel) Call(_ context.Context, request llm.Request) (llm.Result, error) {
	var payload struct {
		Tools []tools.Definition `json:"tools"`
	}
	if err := json.Unmarshal([]byte(request.Messages[1].Content), &payload); err != nil {
		return llm.Result{}, err
	}
	if len(payload.Tools) != 1 || payload.Tools[0].Name != "echo" {
		m.t.Fatalf("bootstrap added an implicit capability: %+v", payload.Tools)
	}
	return llm.Result{Content: `{"name":"echo","arguments":{"text":"ready"}}`}, nil
}

func TestBootstrapUsesOnlySuppliedRegistry(t *testing.T) {
	type input struct {
		Text string `json:"text"`
	}
	type output struct {
		Text string `json:"text"`
	}
	tool := tools.NewTyped(tools.Metadata{Name: "echo", Description: "回显", Terminal: true}, func(_ context.Context, _ tools.Scope, in input) (output, error) { return output{Text: in.Text}, nil }, tools.Options[output]{Present: func(out output) tools.Completion {
		return tools.Completion{Answer: &ai.Answer{Status: "answered", Answer: out.Text, SourceMessageIDs: []string{}}}
	}})
	registry, err := tools.New(tool)
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(customCatalogModel{t: t}, "test", 16384, registry)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := ai.Context{GroupID: "g", ActorID: "u", Users: []ai.User{{ID: "u", Nickname: "User"}}, Messages: []ai.Message{{ID: "m", GroupID: "g", SenderID: "u", Text: "echo", SentAt: time.Now()}}}
	run, err := service.Execute(context.Background(), ai.Invocation{Context: snapshot, Question: "echo"})
	if err != nil || run.Answer == nil || run.Answer.Answer != "ready" {
		t.Fatalf("explicit registry not used: %+v %v", run.Answer, err)
	}
	if definitions := registry.Definitions(); len(definitions) != 1 || definitions[0].Name != "echo" {
		t.Fatal("supplied registry mutated")
	}
}

func TestBootstrapRequiresExplicitRegistry(t *testing.T) {
	if _, err := New(customCatalogModel{t: t}, "test", 16384, nil); err == nil {
		t.Fatal("missing registry silently replaced with defaults")
	}
}

type entryModel struct {
	outputs  []string
	requests []llm.Request
}

func (m *entryModel) Call(_ context.Context, request llm.Request) (llm.Result, error) {
	m.requests = append(m.requests, request)
	if len(m.outputs) == 0 {
		return llm.Result{}, errors.New("unexpected model call")
	}
	output := m.outputs[0]
	m.outputs = m.outputs[1:]
	return llm.Result{Content: output}, nil
}
func TestBootstrapRoutesActiveAndPassiveToSameMain(t *testing.T) {
	const answer = `{"name":"respond","arguments":{"status":"answered","answer":"确认一下地点？","sourceMessageIds":["m"]}}`
	for _, tc := range []struct {
		name, trigger string
		outputs       []string
		silent        bool
	}{
		{name: "active mention", trigger: "mention", outputs: []string{answer}},
		{name: "active followup", outputs: []string{answer}},
		{name: "passive silence", trigger: "proactive", outputs: []string{`{"action":"skip","verification":null,"reason":"正常聊天","breakdowns":[],"intent":null}`}, silent: true},
		{name: "passive intervention", trigger: "proactive", outputs: []string{`{"action":"intervene","verification":null,"reason":"地点不明确","breakdowns":[],"intent":{"type":"clarify","objective":"确认地点","evidenceMessageIds":["m"]}}`, answer}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := &entryModel{outputs: append([]string{}, tc.outputs...)}
			registry, err := tools.New(respond.New(), silence.New())
			if err != nil {
				t.Fatal(err)
			}
			service, err := New(model, "test", 16384, registry)
			if err != nil {
				t.Fatal(err)
			}
			snapshot := ai.Context{GroupID: "g", ActorID: "u", Users: []ai.User{{ID: "u", Nickname: "用户"}}, Messages: []ai.Message{{ID: "m", GroupID: "g", SenderID: "u", Text: "去哪里", SentAt: time.Now()}}, Request: &ai.RequestContext{MessageID: "m"}}
			run, err := service.Execute(context.Background(), ai.Invocation{TriggerMode: tc.trigger, Context: snapshot, Question: "去哪里"})
			if err != nil || run.Status != "succeeded" || run.Silent != tc.silent || len(model.requests) != len(tc.outputs) || run.AgentID != "main" || run.InvocationID == "" {
				t.Fatalf("entry wiring failed: run=%+v calls=%d err=%v", run, len(model.requests), err)
			}
			if (run.Recognition != nil) != (tc.trigger == "proactive") {
				t.Fatal("incorrect recognition path", run)
			}
			if !tc.silent && (run.Answer == nil || run.Answer.Answer != "确认一下地点？") {
				t.Fatal("shared main did not respond", run)
			}
		})
	}
}

func TestBootstrapVerificationHandoffKeepsHistoricalEvidence(t *testing.T) {
	const discovery = `{"action":"verify","reason":"需核实此前预算","breakdowns":[],"intent":null,"verification":{"topic":"本次周末聚餐","query":"此前预算及后续修改是什么","evidenceMessageIds":["m"]}}`
	for _, intervene := range []bool{true, false} {
		t.Run(fmt.Sprint("intervene=", intervene), func(t *testing.T) {
			final := `{"shouldIntervene":false,"reason":"预算已经调整，无需重复参与","breakdowns":[],"intent":null}`
			if intervene {
				final = `{"shouldIntervene":true,"reason":"有明确预算和当前提议的原文","breakdowns":[{"type":"forgotten_constraint","evidenceMessageIds":["old","m"]}],"intent":{"type":"clarify","objective":"确认是否要调整本次预算","evidenceMessageIds":["old","m"]}}`
			}
			model := &entryModel{outputs: []string{discovery, final}}
			if intervene {
				model.outputs = append(model.outputs, `{"name":"respond","arguments":{"status":"answered","answer":"此前人均预算是300元，要调整吗？","sourceMessageIds":["old","m"]}}`)
			}
			registry, err := tools.New(respond.New(), silence.New())
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			snapshot := ai.Context{GroupID: "g", ActorID: "u", Users: []ai.User{{ID: "u", Nickname: "用户"}}, Messages: []ai.Message{{ID: "m", GroupID: "g", SenderID: "u", Text: "准备订500元这家，预算按之前的来", SentAt: now}}, Request: &ai.RequestContext{MessageID: "m", SenderID: "u", SentAt: now}}
			reads := 0
			provider := func(_ context.Context, request ai.InterventionEvidenceRequest) (ai.InterventionEvidence, error) {
				reads++
				if request.Context.GroupID != "g" || request.Context.ActorID != "u" || request.ContextEndSeq != 42 || request.Question.Topic != "本次周末聚餐" {
					t.Fatal("untrusted verification scope", request)
				}
				return ai.InterventionEvidence{Coverage: "partial", Messages: []ai.Message{{ID: "old", GroupID: "g", SenderID: "u", Text: "本次聚餐人均最多300元", SentAt: now.Add(-time.Hour)}}}, nil
			}
			service, err := New(model, "test", 32768, registry, provider)
			if err != nil {
				t.Fatal(err)
			}
			run, err := service.Execute(context.Background(), ai.Invocation{TriggerMode: "proactive", ContextEndSeq: 42, Context: snapshot})
			if err != nil || run.Recognition == nil || run.Silent == intervene || reads != 1 || len(model.outputs) != 0 {
				t.Fatalf("verification pipeline failed: %+v err=%v reads=%d", run, err, reads)
			}
			if run.Steps[0].Name != "discover_intervention" || run.Steps[1].Name != "verify_intervention_evidence" || run.Steps[2].Name != "decide_intervention" {
				t.Fatal("missing staged trace", run.Steps)
			}
			if !intervene {
				return
			}
			var execution struct {
				Context ai.Context `json:"context"`
			}
			if err := json.Unmarshal([]byte(model.requests[2].Messages[1].Content), &execution); err != nil {
				t.Fatal(err)
			}
			if len(execution.Context.Messages) != 2 || execution.Context.Messages[0].ID != "old" || len(execution.Context.Request.RelatedMessageIDs) != 2 {
				t.Fatal("historical evidence lost at main handoff", execution)
			}
			if len(snapshot.Messages) != 1 || len(snapshot.Request.RelatedMessageIDs) != 0 {
				t.Fatal("recognition snapshot mutated")
			}
		})
	}
}
