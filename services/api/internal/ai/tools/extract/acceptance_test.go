package extracttool

import (
	"context"
	"testing"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
)

func TestAcceptanceSupplementRequiresPairAndResponder(t *testing.T) {
	_, w, m := setup(t, "")
	input := ai.Context{Users: []ai.User{{ID: "a", Nickname: "A"}, {ID: "b", Nickname: "B"}}, Messages: []ai.Message{{ID: "r", SenderID: "a", Text: "帮我拿包"}, {ID: "yes", SenderID: "b", Text: "好。"}}}
	for index, body := range []string{
		`{"suggestions":[{"title":"拿包","assigneeId":"a","timeExpression":"","sourceMessageIds":["r","yes"]}]}`,
		`{"suggestions":[{"title":"拿包","assigneeId":"b","timeExpression":"","sourceMessageIds":["yes"]}]}`,
		`{"suggestions":[{"title":"拿包","assigneeId":"b","sourceMessageIds":["r","yes"]}]}`,
	} {
		m.content = body
		got, err := w.supplementAcceptances(context.Background(), &ai.Run{}, input, nil)
		if index < 2 && (err != nil || len(got) != 0) {
			t.Fatalf("unsupported semantic candidate was not filtered: %+v %v", got, err)
		}
		if index == 2 && err == nil {
			t.Fatal("malformed acceptance schema accepted")
		}
	}
	m.content = `{"suggestions":[{"title":"拿包","assigneeId":"b","timeExpression":"","sourceMessageIds":["r","yes"]}]}`
	got, err := w.supplementAcceptances(context.Background(), &ai.Run{}, input, nil)
	if err != nil || len(got) != 1 {
		t.Fatalf("valid accepted action lost: %+v %v", got, err)
	}
	if pairs := uncoveredAcceptancePairs(input, got); len(pairs) != 0 {
		t.Fatal("existing acceptance duplicated")
	}
	input.Users[1].Nickname = "助手(AI)"
	if pairs := uncoveredAcceptancePairs(input, nil); len(pairs) != 0 {
		t.Fatal("AI agreement treated as human acceptance")
	}
}

func TestAcceptanceSupplementKeepsValidCandidateAmongInvalidOnes(t *testing.T) {
	_, w, _ := setup(t, `{"suggestions":[{"title":"拿包","assigneeId":"b","timeExpression":"2026-10-12T00:00:00+08:00","sourceMessageIds":["r","yes"]},{"title":"拿包","assigneeId":"a","timeExpression":"","sourceMessageIds":["r","yes"]}]}`)
	input := ai.Context{Messages: []ai.Message{{ID: "r", SenderID: "a", Text: "帮我拿包"}, {ID: "yes", SenderID: "b", Text: "好"}}}
	got, err := w.supplementAcceptances(context.Background(), &ai.Run{}, input, nil)
	if err != nil || len(got) != 1 || got[0].TimeExpression != "" || got[0].AssigneeID == nil || *got[0].AssigneeID != "b" {
		t.Fatalf("valid candidate lost or fabricated time retained: %+v %v", got, err)
	}
}

func TestTerminalAcceptedActionSkipsOnlyUnneededStateReview(t *testing.T) {
	_, w, m := setup(t, "invalid model response must not be needed")
	owner := "b"
	input := ai.Context{Messages: []ai.Message{{ID: "r", SenderID: "a", Text: "帮我拿包"}, {ID: "yes", SenderID: "b", Text: "好"}}}
	candidate := ai.Suggestion{ID: "accepted", Title: "拿包", AssigneeID: &owner, SourceMessageIDs: []string{"r", "yes"}}
	run := ai.Run{}
	got, err := w.reviewActions(context.Background(), &run, input, []ai.Suggestion{candidate}, nil)
	if err != nil || len(got) != 1 {
		t.Fatalf("terminal acceptance lost: %+v %v", got, err)
	}
	if len(run.Steps) != 1 || run.Steps[0].Name != "terminal_acceptance_state" {
		t.Fatal("terminal acceptance invoked unnecessary state inference")
	}
	// Later evidence and existing tasks must still invoke independent review.
	m.reviewContent = "invalid"
	input.Messages = append(input.Messages, ai.Message{ID: "cancel", SenderID: "b", Text: "不拿了"})
	if _, err = w.reviewActions(context.Background(), &ai.Run{}, input, []ai.Suggestion{candidate}, nil); err == nil {
		t.Fatal("later cancellation bypassed review")
	}
	input.Messages = input.Messages[:2]
	candidate.PossibleDuplicateIDs = []string{"existing"}
	if _, err = w.reviewActions(context.Background(), &ai.Run{}, input, []ai.Suggestion{candidate}, nil); err == nil {
		t.Fatal("existing task bypassed review")
	}
}

func TestDecodeExtractionRejectsNullOrMissingArray(t *testing.T) {
	for _, content := range []string{`{"suggestions":null}`, `{}`} {
		if _, err := decodeExtraction(content); err == nil {
			t.Fatalf("malformed initial response accepted: %s", content)
		}
	}
}
