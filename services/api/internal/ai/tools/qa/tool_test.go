package qa

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
)

type modelStub struct {
	calls   int
	content string
	request llm.Request
}

func (m *modelStub) Call(_ context.Context, r llm.Request) (llm.Result, error) {
	m.calls++
	m.request = r
	return llm.Result{Content: m.content}, nil
}
func snapshot() ai.Context {
	at := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	return ai.Context{GroupID: "g", ActorID: "u", Users: []ai.User{{ID: "u", Nickname: "User"}}, Messages: []ai.Message{{ID: "current", GroupID: "g", SenderID: "u", SentAt: at, Text: "问题"}}, Request: &ai.RequestContext{MessageID: "current", SenderID: "u", SentAt: at, CurrentTime: at, Timezone: "Asia/Shanghai"}}
}
func TestQAToolValidatesRetrievedEvidenceInsteadOfInitialWindow(t *testing.T) {
	m := &modelStub{content: `{"status":"answered","answer":"旧原文","sourceMessageIds":["old"]}`}
	provider := func(_ context.Context, r ContextRequest) (ai.Context, error) {
		in := r.Scope.Context
		in.Messages = append(in.Messages, ai.Message{ID: "old", GroupID: "g", SenderID: "u", SentAt: in.Messages[0].SentAt.Add(-time.Hour), Text: "旧原文"})
		in.HistoryScope = &ai.HistoryScope{Policy: "qa-history-v1", EarlierMessagesOmitted: true, HistoricalSearch: true}
		return in, nil
	}
	tool := New(m, "test", 16384, provider)
	out, err := tool.Invoke(context.Background(), tools.Scope{Context: snapshot(), ContextEndSeq: 10}, marshalArguments("之前说什么？"))
	if err != nil || out.Completion.Answer == nil || len(out.Completion.Sources) != 1 || out.Completion.Sources[0].ID != "old" || !out.Terminal {
		t.Fatal("retrieved citation rejected", err)
	}
	_, text, err := ai.ResultFromRun(ai.Run{Kind: "main", Answer: out.Completion.Answer, Sources: out.Completion.Sources, Basis: out.Completion.Basis, HistoryScope: out.Completion.HistoryScope})
	if err != nil || strings.Contains(text, "未检索更早记录") {
		t.Fatal("retrieval coverage mislabeled", err)
	}
}
func TestQAToolRejectsInvalidAnswersAndCitations(t *testing.T) {
	for _, body := range []string{
		`{"status":"answered","answer":"回答","sourceMessageIds":["foreign"]}`,
		`{"status":"answered","answer":"回答","sourceMessageIds":["current","current"]}`,
		`{"status":"answered","answer":"回答","sourceMessageIds":["current"],"extra":true}`,
		`{"status":"answered","answer":"回答","sourceMessageIds":null}`,
		`not JSON`,
	} {
		model := &modelStub{content: body}
		out, err := New(model, "test", 16384, nil).Invoke(context.Background(), tools.Scope{Context: snapshot()}, marshalArguments("之前说什么？"))
		if err == nil || out.Completion.Answer != nil {
			t.Fatal("invalid answer exposed", body)
		}
	}
}
func TestQAToolBudgetAndArgumentFailuresAvoidAllExternalCalls(t *testing.T) {
	for _, raw := range []json.RawMessage{json.RawMessage(`{"question":"问题","groupId":"other"}`), json.RawMessage(`{"question":""}`), json.RawMessage(`{"question":"问题","context":"general"}`), json.RawMessage(`{"question":"问题","context":"recent"}`), json.RawMessage(`{"question":"问题","context":"history"}`), json.RawMessage(`{"question":"问题","context":"mixed"}`), json.RawMessage(`{"question":"问题","context":"unknown"}`)} {
		m := &modelStub{}
		providers := 0
		tool := New(m, "test", 4096, func(context.Context, ContextRequest) (ai.Context, error) { providers++; return snapshot(), nil })
		_, err := tool.Invoke(context.Background(), tools.Scope{Context: snapshot()}, raw)
		if !errors.Is(err, ai.ErrInvalidDecision) || m.calls != 0 || providers != 0 {
			t.Fatal("invalid arguments caused external work", err)
		}
	}
	m := &modelStub{}
	providers := 0
	tool := New(m, "test", 8192, func(context.Context, ContextRequest) (ai.Context, error) { providers++; return snapshot(), nil })
	in := snapshot()
	in.Messages[0].Text = strings.Repeat("太大", 20000)
	if _, err := tool.Invoke(context.Background(), tools.Scope{Context: in}, marshalArguments("问题")); !errors.Is(err, llm.ErrContextLimit) || providers != 0 || m.calls != 0 {
		t.Fatal("required context bypassed guard", err)
	}
}
func TestQAToolRejectsChangedTrustedScopeOrRequiredEvidence(t *testing.T) {
	for _, change := range []func(*ai.Context){
		func(in *ai.Context) { in.GroupID = "other" },
		func(in *ai.Context) { in.Messages = nil },
		func(in *ai.Context) { in.Messages[0].Text = "changed" },
		func(in *ai.Context) { in.Request.SentAt = in.Request.SentAt.Add(time.Hour) },
		func(in *ai.Context) { in.Request.Timezone = "UTC" },
		func(in *ai.Context) { in.Request.RelatedMessageIDs = []string{"current"} },
	} {
		m := &modelStub{}
		original := snapshot()
		tool := New(m, "test", 16384, func(_ context.Context, r ContextRequest) (ai.Context, error) {
			in := r.Scope.Context
			in.Messages = append([]ai.Message{}, in.Messages...)
			change(&in)
			return in, nil
		})
		if _, err := tool.Invoke(context.Background(), tools.Scope{Context: original}, marshalArguments("问题")); err == nil || m.calls != 0 {
			t.Fatal("provider changed scope/pinned evidence", err)
		}
		if original.Request.Timezone != "Asia/Shanghai" || !original.Request.SentAt.Equal(snapshot().Request.SentAt) || len(original.Request.RelatedMessageIDs) != 0 {
			t.Fatal("provider mutated the original request")
		}
	}
}
func TestGeneralQAAllowsKnowledgeWithoutHistoricalCitations(t *testing.T) {
	m := &modelStub{content: `{"status":"answered","answer":"通风并清洁","sourceMessageIds":[]}`}
	tool := New(m, "test", 16384, nil)
	out, err := tool.Invoke(context.Background(), tools.Scope{Context: snapshot()}, marshalArguments("怎么除味？"))
	if err != nil || out.Completion.Basis != "general" || m.calls != 1 {
		t.Fatal("general QA regressed", err)
	}
}

// marshalArguments builds typed tool inputs for tests.
func marshalArguments(question string) json.RawMessage {
	raw, _ := json.Marshal(Arguments{Question: question})
	return raw
}
