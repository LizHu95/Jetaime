package mainagent

import (
	"context"
	"strings"
	"testing"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/prompts"
)

func TestSnapshotAgentReusesQAAndExtract(t *testing.T) {
	store, workflow, model := setup(t, `{"status":"answered","answer":"测试消息","sourceMessageIds":["m1"]}`)
	input, err := store.Context("daily", "lin")
	if err != nil {
		t.Fatal(err)
	}
	agent, err := NewAgent(model, "test", 8192)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := agent.Execute(context.Background(), input, "qa", "测试问题？")
	if err != nil {
		t.Fatal(err)
	}
	console, err := workflow.Execute(context.Background(), "daily", "lin", "qa", "测试问题？", "")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.PromptVersion != console.PromptVersion || snapshot.Answer.Answer != console.Answer.Answer || string(model.request.Format) != string(prompts.QASchema) {
		t.Fatal("snapshot bypassed existing QA engine")
	}
	model.content = `{"suggestions":[{"title":"测试任务","assigneeId":"lin","timeExpression":"明天","sourceMessageIds":["m6"]}]}`
	extracted, err := agent.Execute(context.Background(), input, "extract", "")
	if err != nil || len(extracted.Suggestions) != 1 {
		t.Fatal("existing extraction unavailable", err)
	}
	_, text, err := ai.ResultFromRun(extracted)
	if err != nil || !strings.Contains(text, "尚未创建") {
		t.Fatal("suggestion pretended to create task")
	}
}
func TestSnapshotAgentRejectsUnauthorizedSourcesAndActor(t *testing.T) {
	store, _, model := setup(t, `{"status":"answered","answer":"bad","sourceMessageIds":["m8"]}`)
	input, _ := store.Context("daily", "lin")
	agent, _ := NewAgent(model, "test", 8192)
	run, err := agent.Execute(context.Background(), input, "qa", "谁说的？")
	if err == nil || run.Answer != nil {
		t.Fatal("cross-group source accepted")
	}
	input.ActorID = "stranger"
	if _, err := agent.Execute(context.Background(), input, "qa", "test"); err == nil {
		t.Fatal("nonmember accepted")
	}
	input.ActorID = "lin"
	input.Messages[0].GroupID = "other"
	if _, err := agent.Execute(context.Background(), input, "qa", "test"); err == nil {
		t.Fatal("foreign message in snapshot")
	}
}
