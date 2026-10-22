package history

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	mainagent "github.com/LizHu95/Jetaime/services/api/internal/ai/agent/main"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
)

type fakeReader struct {
	messages []ai.Message
	calls    int
	err      error
}

func (r *fakeReader) HistoryMessages(_ context.Context, group, actor string, fence int64, start, end time.Time) ([]ai.Message, error) {
	r.calls++
	if group != "group" || actor != "user" || fence != 42 {
		return nil, fmt.Errorf("backend scope lost")
	}
	return r.messages, r.err
}

type fakeModel struct {
	calls            int
	request          llm.Request
	answer           string
	promptTokens     *int
	completionTokens *int
}

func (m *fakeModel) Call(_ context.Context, r llm.Request) (llm.Result, error) {
	m.calls++
	m.request = r
	return llm.Result{Content: m.answer, PromptTokens: m.promptTokens, CompletionTokens: m.completionTokens}, nil
}
func testScope() tools.Scope {
	return tools.Scope{Context: ai.Context{GroupID: "group", ActorID: "user", Users: []ai.User{{ID: "user", Nickname: "用户"}}}, ContextEndSeq: 42, Question: "上周聊了什么"}
}
func testMessages(n int) []ai.Message {
	out := make([]ai.Message, n)
	for i := range out {
		out[i] = ai.Message{ID: fmt.Sprint(i), GroupID: "group", SenderID: "user", SentAt: time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC), Text: "讨论项目"}
	}
	return out
}
func TestHistoryLimitsBeforeSummary(t *testing.T) {
	for _, tc := range []struct {
		name, start, end string
		count, tokens    int
		status           string
		queries, models  int
	}{
		{"week", "2026-10-12T00:00:00Z", "2026-10-19T00:00:00Z", 1, 2000000, "ok", 1, 1},
		{"over_week", "2026-10-12T00:00:00Z", "2026-10-19T00:00:01Z", 1, 2000000, "range_too_large", 0, 0},
		{"year", "2025-01-01T00:00:00+08:00", "2026-01-01T00:00:00+08:00", 1, 2000000, "range_too_large", 0, 0},
		{"1000", "2026-10-12T00:00:00Z", "2026-10-19T00:00:00Z", 1000, 2000000, "ok", 1, 1},
		{"1001", "2026-10-12T00:00:00Z", "2026-10-19T00:00:00Z", 1001, 2000000, "too_many_messages", 1, 0},
		{"token_overflow", "2026-10-12T00:00:00Z", "2026-10-19T00:00:00Z", 1, 2048, "token_limit_exceeded", 1, 0},
		{"empty", "2026-10-12T00:00:00Z", "2026-10-19T00:00:00Z", 0, 2000000, "empty", 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := &fakeReader{messages: testMessages(tc.count)}
			model := &fakeModel{answer: `{"status":"answered","answer":"讨论了项目。","sourceMessageIds":["0"]}`}
			tool := New(model, "test", tc.tokens, reader)
			if _, err := tools.New(tool); err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(Arguments{tc.start, tc.end})
			result, err := tool.Invoke(context.Background(), testScope(), raw)
			if err != nil {
				t.Fatal(err)
			}
			var out Output
			if err = json.Unmarshal(result.Data, &out); err != nil {
				t.Fatal(err)
			}
			if out.Status != tc.status || reader.calls != tc.queries || model.calls != tc.models || !result.Terminal || result.Completion.Answer == nil {
				t.Fatalf("status=%s queries=%d models=%d result=%+v", out.Status, reader.calls, model.calls, result)
			}
			if tc.status == "ok" {
				if len(result.Completion.Sources) != 1 || !strings.Contains(model.request.Messages[1].Content, fmt.Sprintf(`"id":"%d"`, tc.count-1)) {
					t.Fatal("missing evidence or truncated input")
				}
			}
			if tc.status == "ok" || tc.status == "token_limit_exceeded" {
				budget := out.Budget
				if budget == nil || budget.Method != "utf8_byte_upper_bound" || budget.ContextLimitTokens != tc.tokens || budget.OutputReserveTokens != 2048 || budget.AvailableInputTokens != tc.tokens-2048 || budget.InputTokenUpperBound <= 0 || budget.Exceeded != (tc.status == "token_limit_exceeded") {
					t.Fatalf("missing or incorrect budget: %+v", budget)
				}
				if tc.status == "ok" && budget.InputTokenUpperBound != llm.InputTokenUpperBound(model.request) {
					t.Fatal("reported budget does not match model request")
				}
				found := false
				for _, step := range result.Execution.Steps {
					if step.Name == "budget" {
						found = true
						if (step.Status == "failed") != budget.Exceeded {
							t.Fatal("incorrect budget trace status")
						}
					}
				}
				if !found {
					t.Fatal("missing budget trace")
				}
			} else if out.Budget != nil {
				t.Fatal("budget reported before request was built")
			}
			if out.Usage != nil {
				t.Fatal("fabricated provider usage")
			}
		})
	}
}

func TestHistoryReportsProviderUsageSeparatelyFromBudget(t *testing.T) {
	prompt, completion := 123, 45
	model := &fakeModel{answer: `{"status":"answered","answer":"讨论项目。","sourceMessageIds":["0"]}`, promptTokens: &prompt, completionTokens: &completion}
	result, err := New(model, "test", 20000, &fakeReader{messages: testMessages(1)}).Invoke(context.Background(), testScope(), json.RawMessage(`{"startTime":"2026-10-12T00:00:00Z","endTime":"2026-10-19T00:00:00Z"}`))
	if err != nil {
		t.Fatal(err)
	}
	var out Output
	if err := json.Unmarshal(result.Data, &out); err != nil {
		t.Fatal(err)
	}
	if out.Budget == nil || out.Usage == nil || out.Usage.PromptTokens == nil || *out.Usage.PromptTokens != prompt || out.Usage.CompletionTokens == nil || *out.Usage.CompletionTokens != completion {
		t.Fatalf("missing provider usage: %+v", out)
	}
}
func TestHistoryRejectsBadRangesAndMissingFence(t *testing.T) {
	for _, args := range []Arguments{{"bad", "2026-10-19T00:00:00Z"}, {"2026-10-12T00:00:00Z", "2026-10-12T00:00:00Z"}, {"2026-10-12T00:00:00Z", "2026-10-11T00:00:00Z"}} {
		reader := &fakeReader{}
		_, err := execute(context.Background(), testScope(), args, nil, "test", 10000, reader)
		if _, ok := tools.Repairable(err); !ok || reader.calls != 0 {
			t.Fatal("invalid range reached storage", err)
		}
	}
	scope := testScope()
	scope.ContextEndSeq = 0
	_, err := execute(context.Background(), scope, Arguments{"2026-10-12T00:00:00Z", "2026-10-19T00:00:00Z"}, nil, "test", 10000, &fakeReader{})
	if err == nil {
		t.Fatal("unfenced query accepted")
	}
}
func TestHistoryOutputCannotCiteOtherMessages(t *testing.T) {
	for _, ids := range []string{`["unknown"]`, `[]`, `["0","0"]`} {
		reader := &fakeReader{messages: testMessages(1)}
		model := &fakeModel{answer: `{"status":"answered","answer":"总结","sourceMessageIds":` + ids + `}`}
		_, err := execute(context.Background(), testScope(), Arguments{"2026-10-12T00:00:00Z", "2026-10-19T00:00:00Z"}, model, "test", 20000, reader)
		if !errors.Is(err, tools.ErrInvalidOutput) {
			t.Fatal("invalid sources accepted", err)
		}
		if _, ok := tools.Repairable(err); ok {
			t.Fatal("output failure is replayable")
		}
	}
}
func TestHistoryReaderScopeAndFailure(t *testing.T) {
	args := Arguments{"2026-10-12T00:00:00Z", "2026-10-19T00:00:00Z"}
	for _, kind := range []string{"other_group", "before_start", "at_end", "storage"} {
		t.Run(kind, func(t *testing.T) {
			reader := &fakeReader{messages: testMessages(1)}
			switch kind {
			case "other_group":
				reader.messages[0].GroupID = "other"
			case "before_start":
				reader.messages[0].SentAt = reader.messages[0].SentAt.Add(-time.Second)
			case "at_end":
				reader.messages[0].SentAt = reader.messages[0].SentAt.Add(7 * 24 * time.Hour)
			case "storage":
				reader.err = errors.New("unavailable")
			}
			model := &fakeModel{}
			_, err := execute(context.Background(), testScope(), args, model, "test", 20000, reader)
			if err == nil || model.calls != 0 {
				t.Fatal("invalid evidence reached model", err)
			}
		})
	}
}

func TestMainHistoryToolReturnsSourcesOutsideRecentWindow(t *testing.T) {
	reader := &fakeReader{messages: testMessages(1)}
	summaryModel := &fakeModel{answer: `{"status":"answered","answer":"讨论了项目。","sourceMessageIds":["0"]}`}
	router := &fakeModel{answer: `{"name":"summarize_history","arguments":{"startTime":"2026-10-12T00:00:00Z","endTime":"2026-10-19T00:00:00Z"}}`}
	agent, err := mainagent.NewAgent(router, "test", 32768)
	if err != nil {
		t.Fatal(err)
	}
	agent.Tools, err = tools.New(New(summaryModel, "test", 32768, reader))
	if err != nil {
		t.Fatal(err)
	}
	scope := testScope()
	scope.Context.Request = &ai.RequestContext{MessageID: "trigger", SenderID: "user", SentAt: time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC), Timezone: "Asia/Shanghai"}
	scope.Context.Messages = []ai.Message{{ID: "trigger", GroupID: "group", SenderID: "user", SentAt: scope.Context.Request.SentAt, Text: scope.Question}}
	run, err := agent.ExecuteMain(context.Background(), scope.Context, scope.Question, nil, 42)
	if err != nil || router.calls != 1 || summaryModel.calls != 1 || run.Answer == nil || len(run.Sources) != 1 || run.Sources[0].ID != "0" || run.HistoryScope == nil || run.HistoryScope.EarlierMessagesOmitted {
		t.Fatalf("history terminal result lost: %+v err=%v", run, err)
	}
}
