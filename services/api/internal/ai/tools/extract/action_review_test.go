package extracttool

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
)

func TestActionReviewFiltersWithoutChangingCandidates(t *testing.T) {
	candidates := []ai.Suggestion{{Title: "已完成"}, {Title: "保留"}, {Title: "邀请"}, {Title: "不确定"}, {Title: "取消"}}
	messages := map[string]ai.Message{"original": {ID: "original", GroupID: "g"}, "later": {ID: "later", GroupID: "g"}}
	reviews := []actionReview{
		{4, "cancelled", []string{"later"}, "状态依据"},
		{0, "completed", []string{"later"}, "状态依据"},
		{2, "uncommitted", []string{"original"}, "状态依据"},
		{1, "active", []string{"original"}, "状态依据"},
		{3, "uncertain", []string{"original"}, "状态依据"},
	}
	got, err := filterReviewedActions(candidates, reviews, "g", messages)
	if err != nil || len(got) != 1 || got[0].Title != "保留" || len(candidates) != 5 {
		t.Fatalf("unexpected filtering: %+v %v", got, err)
	}
}

func TestActionReviewRejectsUnverifiableReview(t *testing.T) {
	candidates := []ai.Suggestion{{Title: "一"}, {Title: "二"}}
	messages := map[string]ai.Message{"a": {ID: "a", GroupID: "g"}, "other": {ID: "other", GroupID: "other"}}
	valid := actionReview{0, "active", []string{"a"}, "状态依据"}
	for name, reviews := range map[string][]actionReview{
		"missing": {valid}, "duplicate": {valid, valid},
		"unknown source":   {valid, {1, "active", []string{"unknown"}, "状态依据"}},
		"cross group":      {valid, {1, "active", []string{"other"}, "状态依据"}},
		"unknown status":   {valid, {1, "guess", []string{"a"}, "状态依据"}},
		"missing evidence": {valid, {1, "active", nil, "状态依据"}},
		"out of range":     {valid, {2, "active", []string{"a"}, "状态依据"}},
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := filterReviewedActions(candidates, reviews, "g", messages); err == nil || got != nil {
				t.Fatalf("unverified suggestions accepted: %+v %v", got, err)
			}
		})
	}
}

func TestMalformedActionReviewCannotExposeSuggestions(t *testing.T) {
	for _, review := range []string{
		`{"reviews":[]}`,
		`{"reviews":[{"status":"active","evidenceMessageIds":["m6"]}]}`,
		`{"reviews":[{"candidateIndex":null,"status":"active","evidenceMessageIds":["m6"]}]}`,
		`{"reviews":[{"candidateIndex":0,"status":"active","evidenceMessageIds":["m6"],"reason":"依据","extra":true}]}`,
	} {
		input, w, m := setup(t, `{"suggestions":[{"title":"测试任务","assigneeId":"lin","timeExpression":"明天","sourceMessageIds":["m6"]}]}`)
		m.reviewContent = review
		run, err := executeExtractionTool(context.Background(), w, input, "")
		if err == nil || run.Status != "failed" || len(run.Suggestions) != 0 {
			t.Fatalf("unverified candidates exposed: %+v %v", run, err)
		}
	}
}

type budgetProbeModel struct{ calls int }

func (m *budgetProbeModel) Call(context.Context, llm.Request) (llm.Result, error) {
	m.calls++
	return llm.Result{}, nil
}

func TestExtractionStagesRejectBudgetBeforeModel(t *testing.T) {
	for _, stage := range []string{"extract", "acceptance", "review"} {
		t.Run(stage, func(t *testing.T) {
			input, workflow, _ := setup(t, `{}`)
			input.Messages[0].Text = strings.Repeat("超长历史", 5000)
			probe := &budgetProbeModel{}
			workflow.ChatModel = probe
			var err error
			switch stage {
			case "extract":
				_, err = executeExtractionTool(context.Background(), workflow, input, "问题")
			case "acceptance":
				input.Messages[1].SenderID = "xia"
				input.Messages[1].Text = "好"
				_, err = workflow.supplementAcceptances(context.Background(), &ai.Run{Model: "test"}, input, nil)
			case "review":
				_, err = workflow.reviewUnresolvedActions(context.Background(), &ai.Run{Model: "test"}, input, []ai.Suggestion{{Title: "测试", SourceMessageIDs: []string{"m1"}}}, nil)
			}
			if !errors.Is(err, ai.ErrContext) || probe.calls != 0 {
				t.Fatalf("budget guard bypassed at %s: calls=%d error=%v", stage, probe.calls, err)
			}
		})
	}
}
