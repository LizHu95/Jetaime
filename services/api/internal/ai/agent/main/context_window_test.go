package mainagent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	aicontext "github.com/LizHu95/Jetaime/services/api/internal/ai/context"
	"github.com/LizHu95/Jetaime/services/api/internal/llm"
)

func TestRecentQAContextBoundsCompleteMessagesAndSources(t *testing.T) {
	store, _, model := setup(t, `{}`)
	input, err := store.Context("daily", "lin")
	if err != nil {
		t.Fatal(err)
	}
	seed := input.Messages[0]
	input.Messages = nil
	for i := 0; i < 100; i++ {
		message := seed
		message.ID = fmt.Sprintf("long-%d", i)
		message.Text = strings.Repeat("中文历史<引用>\n", 30)
		input.Messages = append(input.Messages, message)
	}
	question := "最近说了什么？"
	selected, err := aicontext.RecentQAContext(input, question, 8192, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected.Messages) == 0 || len(selected.Messages) >= len(input.Messages) || !selected.HistoryScope.EarlierMessagesOmitted {
		t.Fatal("large history was not bounded", selected.HistoryScope)
	}
	start := len(input.Messages) - len(selected.Messages)
	for i, message := range selected.Messages {
		if message != input.Messages[start+i] {
			t.Fatal("message was cut, skipped or reordered")
		}
	}
	if input.HistoryScope != nil || len(input.Messages) != 100 {
		t.Fatal("original history was mutated")
	}
	model.content = `{"status":"answered","answer":"近期消息","sourceMessageIds":["long-99"]}`
	agent, _ := NewAgent(model, "test", 8192)
	run, err := agent.Execute(context.Background(), selected, "qa", question)
	if err != nil {
		t.Fatal("selected context failed workflow budget", err)
	}
	if err := llm.CheckContextBudget(model.request, 8192); err != nil || !strings.Contains(model.request.Messages[0].Content, "未检索更早历史") {
		t.Fatal("request budget or coverage instruction missing", err)
	}
	result, text, err := ai.ResultFromRun(run)
	if err != nil || result.HistoryScope == nil || !strings.Contains(text, "未检索更早记录") {
		t.Fatal("coverage missing from durable reply", err)
	}
	model.content = `{"status":"answered","answer":"旧消息","sourceMessageIds":["long-0"]}`
	if _, err := agent.Execute(context.Background(), selected, "qa", question); err == nil {
		t.Fatal("source outside selected evidence accepted")
	}
}

type budgetProbeModel struct{ calls int }

func (m *budgetProbeModel) Call(context.Context, llm.Request) (llm.Result, error) {
	m.calls++
	return llm.Result{}, nil
}

func TestAllWorkflowStagesRejectBudgetBeforeModel(t *testing.T) {
	for _, stage := range []string{"qa", "extract", "acceptance", "review"} {
		t.Run(stage, func(t *testing.T) {
			store, workflow, _ := setup(t, `{}`)
			input, _ := store.Context("daily", "lin")
			input.Messages[0].Text = strings.Repeat("超长历史", 5000)
			probe := &budgetProbeModel{}
			workflow.ChatModel = probe
			var err error
			switch stage {
			case "qa", "extract":
				_, err = workflow.execute(context.Background(), input, stage, "问题", "test")
			case "acceptance":
				input.Messages[1].SenderID = "xia"
				input.Messages[1].Text = "好"
				_, err = workflow.supplementAcceptances(context.Background(), &ai.Run{Model: "test"}, input, nil)
			case "review":
				_, err = workflow.reviewUnresolvedActions(context.Background(), &ai.Run{Model: "test"}, input, []ai.Suggestion{{Title: "测试", SourceMessageIDs: []string{"m1"}}}, nil)
			}
			if !errors.Is(err, ErrBudget) || probe.calls != 0 {
				t.Fatalf("budget guard bypassed at %s: calls=%d error=%v", stage, probe.calls, err)
			}
		})
	}
}

func TestRecentQAContextSmallHistoryAndOversizedTrigger(t *testing.T) {
	store, _, _ := setup(t, `{}`)
	input, _ := store.Context("daily", "lin")
	selected, err := aicontext.RecentQAContext(input, "问题", 32768, false)
	if err != nil || len(selected.Messages) != len(input.Messages) || selected.HistoryScope.EarlierMessagesOmitted {
		t.Fatal("small history unnecessarily dropped", err)
	}
	selected, err = aicontext.RecentQAContext(input, "问题", 32768, true)
	if err != nil || !selected.HistoryScope.EarlierMessagesOmitted {
		t.Fatal("database coverage flag lost", err)
	}
	input.Messages[len(input.Messages)-1].Text = strings.Repeat("过长", 10000)
	if _, err := aicontext.RecentQAContext(input, "问题", 8192, false); !errors.Is(err, ErrBudget) {
		t.Fatal("oversized trigger silently dropped", err)
	}
}
