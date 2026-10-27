package qa

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	aicontext "github.com/LizHu95/Jetaime/services/api/internal/ai/context"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/execution"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/memory"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/respond"
	"github.com/LizHu95/Jetaime/services/api/internal/chat/history"
)

type initialHistory struct {
	pending   []memory.Message
	excluded  []string
	documents []memory.Document
	calls     int
	request   memory.SearchInput
	fence     int64
	err       error
}

func (h *initialHistory) SearchDocuments(_ context.Context, group, actor string, in memory.SearchInput) (memory.SearchResult, error) {
	if group != "g" || actor != "u" {
		return memory.SearchResult{}, errors.New("wrong authorization")
	}
	h.calls++
	h.request = in
	docs := h.documents
	if docs == nil {
		docs = []memory.Document{{SourceIDs: []string{"old"}}}
	}
	return memory.SearchResult{Documents: docs}, h.err
}
func (h *initialHistory) UnprocessedMessages(_ context.Context, group, actor string, in memory.SearchInput, exclude []string) (memory.SearchResult, error) {
	if group != "g" || actor != "u" || in.MaxSeq != h.fence {
		return memory.SearchResult{}, errors.New("wrong pending scope")
	}
	h.excluded = append([]string{}, exclude...)
	return memory.SearchResult{RecentUnprocessed: h.pending}, nil
}
func (h *initialHistory) SourceEvidence(_ context.Context, group, actor string, fence int64, ids []string) (history.SourceEvidence, error) {
	if group != "g" || actor != "u" || fence != 99 || len(ids) != 1 || ids[0] != "old" {
		return history.SourceEvidence{}, errors.New("wrong source scope")
	}
	h.fence = fence
	return history.SourceEvidence{Sources: []history.Message{{ID: "old", Seq: 1, SenderID: "u", Text: "历史中约定周五面试", SentAt: snapshot().Messages[0].SentAt.Add(-time.Hour)}}}, nil
}
func initialRequest() aicontext.ContextRequest {
	return aicontext.ContextRequest{Snapshot: snapshot(), TriggerID: "current", Question: "面试是哪天？", Tokens: 16384, MaxSeq: 99}
}

type routeHistoryModel struct {
	t       *testing.T
	history *initialHistory
	direct  bool
	calls   int
}

func (m *routeHistoryModel) Call(_ context.Context, req llm.Request) (llm.Result, error) {
	m.calls++
	if strings.Contains(req.Messages[0].Content, "主Agent") {
		if m.history.calls != 0 || m.history.fence != 0 || strings.Contains(req.Messages[1].Content, "历史中约定周五面试") {
			m.t.Fatal("history loaded before route selection")
		}
		if m.direct {
			return llm.Result{Content: `{"name":"respond","arguments":{"status":"answered","answer":"你好！","sourceMessageIds":[]}}`}, nil
		}
		return llm.Result{Content: `{"name":"answer_question","arguments":{"question":"面试是哪天？"}}`}, nil
	}
	if m.history.calls != 1 || !strings.Contains(req.Messages[1].Content, "历史中约定周五面试") || !strings.Contains(req.Messages[1].Content, "必须保留的引用") {
		m.t.Fatal("QA missing retrieved or pinned evidence")
	}
	return llm.Result{Content: `{"status":"answered","answer":"周五面试。","sourceMessageIds":["old"]}`}, nil
}

func TestHistoryRetrievalOnlyAfterQAToolSelected(t *testing.T) {
	for _, direct := range []bool{true, false} {
		t.Run(map[bool]string{true: "direct", false: "qa"}[direct], func(t *testing.T) {
			h := &initialHistory{}
			if direct {
				h.err = errors.New("history offline")
			}
			request := initialRequest()
			reference := request.Snapshot.Messages[0]
			reference.ID, reference.Text = "reference", "必须保留的引用"
			request.References = []ai.Message{reference}
			input, err := aicontext.InjectDefaultContext(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			if input.HistoryScope.HistoricalSearch || h.calls != 0 {
				t.Fatal("initial context unexpectedly retrieved history")
			}
			model := &routeHistoryModel{t: t, history: h, direct: direct}
			registry, err := tools.New(respond.New(), New(model, "test", 16384, HistoryContext(h, h)))
			if err != nil {
				t.Fatal(err)
			}
			service, err := execution.New(model, "test", 16384, registry)
			if err != nil {
				t.Fatal(err)
			}
			run, err := service.Execute(context.Background(), ai.Invocation{AgentID: "main", Mode: "main", Question: request.Question, Context: input, ContextEndSeq: 99})
			if err != nil || run.Answer == nil {
				t.Fatal("answer failed", err)
			}
			if direct {
				if model.calls != 1 || h.calls != 0 || run.HistoryScope != nil {
					t.Fatal("direct answer retrieved history")
				}
				return
			}
			if model.calls != 2 || h.calls != 1 || h.request.MaxSeq != 99 || h.request.Limit != 5 || h.request.Kind != "history" || h.request.Query != request.Question || len(run.Sources) != 1 || run.Sources[0].ID != "old" || !run.HistoryScope.HistoricalSearch {
				t.Fatalf("QA retrieval scope or result lost: %+v %+v", h, run)
			}
		})
	}
}

func TestOnDemandHistoryFailsClosed(t *testing.T) {
	h := &initialHistory{}
	request := initialRequest()
	input, err := aicontext.InjectDefaultContext(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	model := &modelStub{}
	tool := New(model, "test", 16384, HistoryContext(h, h))
	scope := tools.Scope{Context: input, Question: request.Question}
	raw, _ := json.Marshal(Arguments{Question: request.Question})
	if _, err = tool.Invoke(context.Background(), scope, raw); err == nil || h.calls != 0 || model.calls != 0 {
		t.Fatal("unfenced retrieval reached storage/model", err)
	}
	scope.ContextEndSeq = 99
	h.err = errors.New("retrieval offline")
	if _, err = tool.Invoke(context.Background(), scope, raw); !errors.Is(err, h.err) || model.calls != 0 {
		t.Fatal("retrieval failure not propagated", err)
	}
}

func TestHistoryContextUsesPendingOriginalsAndDeduplicatesSourceIDs(t *testing.T) {
	h := &initialHistory{documents: []memory.Document{{SourceIDs: []string{"old", "old"}}, {SourceIDs: []string{"old"}}}, pending: []memory.Message{{ID: "update", Seq: 98, SenderID: "u", Text: "面试改到周六", SentAt: snapshot().Messages[0].SentAt.Add(-time.Minute)}}}
	before := snapshot()
	input, err := HistoryContext(h, h)(context.Background(), ContextRequest{Scope: tools.Scope{Context: before, ContextEndSeq: 99, Question: "面试哪天"}, Arguments: Arguments{Question: "面试哪天"}})
	if err != nil || len(input.Messages) != 3 {
		t.Fatal(input, err)
	}
	excluded := map[string]bool{}
	for _, id := range h.excluded {
		excluded[id] = true
	}
	if !excluded["current"] || !excluded["old"] {
		t.Fatal("known originals not excluded before pending query", h.excluded)
	}
	if len(input.HistoryScope.RetrievedMessageIDs) != 1 || len(before.Messages) != 1 {
		t.Fatal("duplicate sources or mutated context", input)
	}
	if input.Messages[1].ID != "update" {
		t.Fatal("pending original missing or not chronological", input.Messages)
	}
}
