package mainagent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
	"github.com/google/jsonschema-go/jsonschema"
)

type repairModel struct {
	responses []string
	requests  []llm.Request
}

func (m *repairModel) Call(_ context.Context, r llm.Request) (llm.Result, error) {
	m.requests = append(m.requests, r)
	index := min(len(m.requests)-1, len(m.responses)-1)
	return llm.Result{Content: m.responses[index]}, nil
}

type saveInput struct {
	Mode string `json:"mode"`
}
type saveOutput struct {
	Status string `json:"status"`
}

func saveContractOptions() tools.Options[saveOutput] {
	return tools.Options[saveOutput]{Input: func(s *jsonschema.Schema) { s.Properties["mode"].Enum = []any{"save"} }, Output: func(s *jsonschema.Schema) { s.Properties["status"].Enum = []any{"saved"} }, Present: func(saveOutput) tools.Completion {
		return tools.Completion{Answer: &ai.Answer{Status: "answered", Answer: "已保存", SourceMessageIDs: []string{}}}
	}}
}
func TestMainRepairsArgumentsWithoutExecutingInvalidCall(t *testing.T) {
	model := &repairModel{responses: []string{`{"name":"save_item","arguments":{"mode":"wrong"}}`, `{"name":"save_item","arguments":{"mode":"save"}}`}}
	saves := 0
	tool := tools.NewTyped(tools.Metadata{Name: "save_item", Description: "保存", Terminal: true}, func(context.Context, tools.Scope, saveInput) (saveOutput, error) {
		saves++
		return saveOutput{Status: "saved"}, nil
	}, saveContractOptions())
	agent, _ := NewAgent(model, "test", 16384)
	var err error
	agent.Tools, err = tools.New(tool)
	if err != nil {
		t.Fatal(err)
	}
	run, err := agent.ExecuteMain(context.Background(), testContext(), "保存", nil)
	if err != nil || saves != 1 || len(model.requests) != 2 || run.Answer == nil || run.Answer.Answer != "已保存" {
		t.Fatalf("repair failed: saves=%d calls=%d err=%v", saves, len(model.requests), err)
	}
	messages := model.requests[1].Messages
	if len(messages) != 3 || !strings.Contains(messages[2].Content, "invalid_arguments") {
		t.Fatal("model did not receive structured validation feedback")
	}
	failed, success := 0, 0
	for _, step := range run.Steps {
		if step.Name == "tool_result" {
			if step.Status == "failed" {
				failed++
			} else {
				success++
			}
		}
	}
	if failed != 1 || success != 1 {
		t.Fatalf("execution records lost: failed=%d succeeded=%d", failed, success)
	}
}
func TestMainRepairsEvidenceBeforeWriting(t *testing.T) {
	model := &repairModel{responses: []string{`{"name":"save_item","arguments":{"mode":"save"}}`}}
	attempts, saves := 0, 0
	tool := tools.NewTyped(tools.Metadata{Name: "save_item", Description: "保存", Terminal: true}, func(context.Context, tools.Scope, saveInput) (saveOutput, error) {
		attempts++
		if attempts == 1 {
			return saveOutput{}, tools.InvalidArguments("time_evidence_missing", "请引用已提供的时间原文。")
		}
		saves++
		return saveOutput{Status: "saved"}, nil
	}, saveContractOptions())
	agent, _ := NewAgent(model, "test", 16384)
	agent.Tools, _ = tools.New(tool)
	_, err := agent.ExecuteMain(context.Background(), testContext(), "保存", nil)
	if err != nil || attempts != 2 || saves != 1 || len(model.requests) != 2 {
		t.Fatal("evidence repair did not respect write boundary", err)
	}
	if !strings.Contains(model.requests[1].Messages[2].Content, "time_evidence_missing") {
		t.Fatal("evidence failure was not fed back")
	}
}
func TestMainDoesNotReplayInfrastructureOrOutputFailures(t *testing.T) {
	for _, invalidOutput := range []bool{false, true} {
		t.Run(map[bool]string{false: "infrastructure", true: "output contract"}[invalidOutput], func(t *testing.T) {
			model := &repairModel{responses: []string{`{"name":"save_item","arguments":{"mode":"save"}}`}}
			saves := 0
			tool := tools.NewTyped(tools.Metadata{Name: "save_item", Description: "保存", Terminal: true}, func(context.Context, tools.Scope, saveInput) (saveOutput, error) {
				saves++
				if invalidOutput {
					return saveOutput{Status: "invalid"}, nil
				}
				return saveOutput{}, errors.New("storage unavailable")
			}, saveContractOptions())
			agent, _ := NewAgent(model, "test", 16384)
			agent.Tools, _ = tools.New(tool)
			run, err := agent.ExecuteMain(context.Background(), testContext(), "保存", nil)
			if err == nil || saves != 1 || len(model.requests) != 1 || run.Answer != nil {
				t.Fatal("potential write was replayed or exposed", err)
			}
			if invalidOutput && !errors.Is(err, tools.ErrInvalidOutput) {
				t.Fatal("output error category lost", err)
			}
		})
	}
}
func TestMainBoundsArgumentRepairAttempts(t *testing.T) {
	model := &repairModel{responses: []string{`{"name":"save_item","arguments":{"mode":"wrong"}}`}}
	saves := 0
	tool := tools.NewTyped(tools.Metadata{Name: "save_item", Description: "保存", Terminal: true}, func(context.Context, tools.Scope, saveInput) (saveOutput, error) {
		saves++
		return saveOutput{Status: "saved"}, nil
	}, saveContractOptions())
	agent, _ := NewAgent(model, "test", 16384)
	agent.Tools, _ = tools.New(tool)
	_, err := agent.ExecuteMain(context.Background(), testContext(), "保存", nil)
	if !errors.Is(err, ai.ErrInvalidDecision) || saves != 0 || len(model.requests) != MaxToolSteps {
		t.Fatal("argument repair loop was not bounded", err)
	}
}
