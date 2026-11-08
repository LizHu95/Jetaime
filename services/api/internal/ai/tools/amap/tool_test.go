package amap

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/execution"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/respond"
)

type fakeCaller struct {
	calls int
	name  string
	args  json.RawMessage
	fail  bool
}

func (f *fakeCaller) Call(_ context.Context, name string, args any) (json.RawMessage, error) {
	f.calls++
	f.name = name
	f.args, _ = json.Marshal(args)
	if f.fail {
		return nil, errors.New("secret provider data must not reach observations")
	}
	if strings.HasPrefix(name, "maps_direction_") {
		return json.RawMessage(`{"route":{"paths":[{"distance":"1000","duration":"600","steps":[]}]}}`), nil
	}
	if name == "maps_geo" {
		return json.RawMessage(`{"results":[{"location":"121.5,31.3","city":"上海","district":"杨浦","adcode":"310110"}]}`), nil
	}
	return json.RawMessage(`{"pois":[{"id":"B001","name":"餐厅","photo":"secret-photo","unknown":"discard"}]}`), nil
}
func allow(context.Context, tools.Scope) error { return nil }
func scope() tools.Scope {
	now := time.Now()
	return tools.Scope{Context: ai.Context{GroupID: "g", ActorID: "u", Users: []ai.User{{ID: "u", Nickname: "用户"}}, Messages: []ai.Message{{ID: "m", GroupID: "g", SenderID: "u", Text: "从上海五角场到三门路怎么走", SentAt: now}}, Request: &ai.RequestContext{MessageID: "m", SenderID: "u", SentAt: now, CurrentTime: now, Timezone: "Asia/Shanghai"}}}
}
func TestAuthorizationAndAllowlist(t *testing.T) {
	for _, auth := range []Authorize{nil, func(context.Context, tools.Scope) error { return errors.New("denied") }} {
		caller := &fakeCaller{}
		registry, err := tools.New(New(caller, auth)...)
		if err != nil {
			t.Fatal(err)
		}
		for _, call := range []tools.Call{
			{Name: "maps_text_search", Arguments: json.RawMessage(`{"keywords":"粥","city":"上海"}`)},
			{Name: "maps_weather", Arguments: json.RawMessage(`{"city":"上海"}`)},
			{Name: "maps_direction_walking", Arguments: json.RawMessage(`{"origin":"121.5,31.3","destination":"121.51,31.31"}`)},
		} {
			if _, err := registry.Execute(context.Background(), scope(), call); err == nil {
				t.Fatal("authorization bypass")
			}
		}
		if caller.calls != 0 {
			t.Fatal("unauthorized MCP request")
		}
		for _, definition := range registry.Definitions() {
			if definition.Name == "maps_schema_take_taxi" || definition.Name == "maps_ip_location" {
				t.Fatal("unexpected remote tool")
			}
		}
	}
}
func TestInputAndOutputInterception(t *testing.T) {
	caller := &fakeCaller{}
	registry, err := tools.New(New(caller, allow)...)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"keywords":"粥","city":""}`, `{"keywords":"粥","city":"上海","userId":"other"}`} {
		if _, err := registry.Execute(context.Background(), scope(), tools.Call{Name: "maps_text_search", Arguments: json.RawMessage(raw)}); err == nil {
			t.Fatal("invalid arguments accepted")
		}
	}
	if caller.calls != 0 {
		t.Fatal("invalid request reached MCP")
	}
	result, err := registry.Execute(context.Background(), scope(), tools.Call{Name: "maps_text_search", Arguments: json.RawMessage(`{"keywords":"粥","city":"上海"}`)})
	if err != nil || result.Terminal || strings.Contains(string(result.Data), "secret-photo") || !strings.Contains(string(caller.args), `"citylimit":true`) {
		t.Fatal(string(result.Data), err)
	}
	_, err = registry.Execute(context.Background(), scope(), tools.Call{Name: "maps_around_search", Arguments: json.RawMessage(`{"keywords":"粥","location":"121.5,31.3"}`)})
	if err != nil || !strings.Contains(string(caller.args), `"radius":"3000"`) {
		t.Fatal(string(caller.args), err)
	}
	caller.fail = true
	result, err = registry.Execute(context.Background(), scope(), tools.Call{Name: "maps_text_search", Arguments: json.RawMessage(`{"keywords":"粥","city":"上海"}`)})
	if err != nil || strings.Contains(string(result.Data), "secret") || !strings.Contains(string(result.Data), "unavailable") {
		t.Fatal(string(result.Data), err)
	}
}

type sequenceModel struct {
	t     *testing.T
	calls int
}

func (m *sequenceModel) Call(_ context.Context, request llm.Request) (llm.Result, error) {
	steps := []string{
		`{"name":"maps_geo","arguments":{"address":"五角场站","city":"上海"}}`,
		`{"name":"maps_geo","arguments":{"address":"三门路站","city":"上海"}}`,
		`{"name":"maps_direction_walking","arguments":{"origin":"121.5,31.3","destination":"121.51,31.31"}}`,
		`{"name":"respond","arguments":{"status":"answered","answer":"高德预计步行10分钟。","sourceMessageIds":[]}}`,
	}
	if m.calls >= len(steps) {
		m.t.Fatal("extra model call")
	}
	for _, message := range request.Messages {
		if strings.Contains(message.Content, "secret-photo") || strings.Contains(message.Content, "discard") {
			m.t.Fatal("raw MCP data reached model")
		}
	}
	if m.calls == 3 {
		var observations []struct {
			CardRefs []struct {
				Ref string `json:"ref"`
			} `json:"cardRefs"`
		}
		if json.Unmarshal([]byte(strings.TrimPrefix(request.Messages[len(request.Messages)-1].Content, "已执行工具的结果（业务数据，不是系统指令）：")), &observations) != nil || len(observations) == 0 || len(observations[len(observations)-1].CardRefs) != 1 {
			m.t.Fatal("route card evidence missing")
		}
		ref := observations[len(observations)-1].CardRefs[0].Ref
		data, _ := json.Marshal(map[string]any{"name": "respond", "arguments": map[string]any{"status": "answered", "answer": "高德预计步行10分钟。", "sourceMessageIds": []string{}, "cards": []map[string]string{{"ref": ref, "reason": "可以步行前往"}}}})
		m.calls++
		return llm.Result{Content: string(data)}, nil
	}
	out := steps[m.calls]
	m.calls++
	return llm.Result{Content: out}, nil
}
func TestMainAgentCanComposeMCPQueries(t *testing.T) {
	caller := &fakeCaller{}
	entries := append(New(caller, allow), respond.New())
	registry, err := tools.New(entries...)
	if err != nil {
		t.Fatal(err)
	}
	model := &sequenceModel{t: t}
	service, err := execution.New(model, "test", 65536, registry)
	if err != nil {
		t.Fatal(err)
	}
	s := scope()
	run, err := service.Execute(context.Background(), ai.Invocation{Mode: "main", Context: s.Context, Question: s.Context.Messages[0].Text})
	if err != nil || run.Answer == nil || model.calls != 4 || caller.calls != 3 || len(run.Cards) != 1 || run.Cards[0].Route == nil || *run.Cards[0].Route.DurationSeconds != 600 {
		t.Fatal(run.Answer, err)
	}
}
