package mainagent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/LizHu95/Jetaime/services/api/internal/ai/prompts"
)

func TestMalformedModelOutputRejected(t *testing.T) {
	for _, output := range []string{`{"status":"answered","answer":"没有来源","sourceMessageIds":[]}`, `{"status":"answered","answer":"来源重复","sourceMessageIds":["m1","m1"]}`, `{"status":"answered","answer":"a","sourceMessageIds":["m1"],"extra":1}`, `not JSON`} {
		_, w, _ := setup(t, output)
		r, err := w.Execute(context.Background(), "daily", "lin", "qa", "测试问题？", "")
		if err == nil || r.Answer != nil {
			t.Fatalf("accepted: %s", output)
		}
	}
}

func TestRequestSchemasValid(t *testing.T) {
	for _, schema := range []json.RawMessage{prompts.QASchema, prompts.ExtractSchema, actionReviewSchema} {
		if !json.Valid(schema) {
			t.Fatalf("invalid schema: %s", schema)
		}
	}
	_, w, m := setup(t, `{"suggestions":[]}`)
	if _, err := w.Execute(context.Background(), "daily", "lin", "extract", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := json.Marshal(m.request); err != nil {
		t.Fatal(err)
	}
}

func TestMalformedActionReviewCannotExposeSuggestions(t *testing.T) {
	for _, review := range []string{
		`{"reviews":[]}`,
		`{"reviews":[{"status":"active","evidenceMessageIds":["m6"]}]}`,
		`{"reviews":[{"candidateIndex":null,"status":"active","evidenceMessageIds":["m6"]}]}`,
		`{"reviews":[{"candidateIndex":0,"status":"active","evidenceMessageIds":["m6"],"reason":"依据","extra":true}]}`,
	} {
		_, w, m := setup(t, `{"suggestions":[{"title":"测试任务","assigneeId":"lin","timeExpression":"明天","sourceMessageIds":["m6"]}]}`)
		m.reviewContent = review
		run, err := w.Execute(context.Background(), "daily", "lin", "extract", "", "")
		if err == nil || run.Status != "failed" || len(run.Suggestions) != 0 {
			t.Fatalf("unverified candidates exposed: %+v %v", run, err)
		}
	}
}
