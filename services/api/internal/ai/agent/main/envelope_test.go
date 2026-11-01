package mainagent

import (
	"context"
	"testing"

	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/respond"
)

func TestWeatherBareAnswerCompletesWithoutModelRepair(t *testing.T) {
	reads := 0
	weather := tools.NewTyped(tools.Metadata{Name: "maps_weather", Description: "天气查询", ReadOnly: true}, func(context.Context, tools.Scope, struct {
		City string `json:"city"`
	}) (struct {
		Forecast string `json:"forecast"`
	}, error) {
		reads++
		return struct {
			Forecast string `json:"forecast"`
		}{Forecast: "明天晴"}, nil
	})
	registry, err := tools.New(weather, respond.New())
	if err != nil {
		t.Fatal(err)
	}
	model := &proactiveModel{outputs: []string{
		`{"name":"maps_weather","arguments":{"city":"上海"}}`,
		`{"status":"answered","answer":"上海明天晴，来源高德天气。","sourceMessageIds":[]}`,
	}}
	agent, _ := NewAgent(model, "test", 32768, registry)
	run, err := agent.ExecuteMain(context.Background(), testContext(), "上海明天天气", nil)
	if err != nil || reads != 1 || len(model.requests) != 2 || run.Answer == nil || run.Answer.Answer != "上海明天晴，来源高德天气。" {
		t.Fatalf("format repair still required: reads=%d models=%d run=%+v err=%v", reads, len(model.requests), run, err)
	}
	normalizations := 0
	for _, step := range run.Steps {
		if step.Name == "normalize_tool_call" {
			normalizations++
		}
		if step.Name == "validate_result" && step.Status == "failed" {
			t.Fatal("normalization was recorded as a model-repair failure")
		}
	}
	if normalizations != 1 {
		t.Fatal("missing explicit normalization audit")
	}
}

func TestBareAnswerCannotPublishForeignSources(t *testing.T) {
	model := &proactiveModel{outputs: []string{`{"status":"answered","answer":"无依据回答","sourceMessageIds":["foreign"]}`}}
	registry, _ := tools.New(respond.New())
	agent, _ := NewAgent(model, "test", 32768, registry)
	run, err := agent.ExecuteMain(context.Background(), testContext(), "回答", nil)
	if err == nil || run.Answer != nil {
		t.Fatal("normalization bypassed source verification")
	}
	found := false
	for _, step := range run.Steps {
		if step.Name == "tool_result" && step.Status == "failed" {
			found = true
		}
	}
	if !found {
		t.Fatal("source rejection was not preserved")
	}
}
