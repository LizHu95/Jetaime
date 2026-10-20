package qa

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/bootstrap"
	aicontext "github.com/LizHu95/Jetaime/services/api/internal/ai/context"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/memory"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/respond"
)

type initialHistory struct {
	calls   int
	request memory.SearchInput
	fence   int64
	err     error
}

func (h *initialHistory) Search(_ context.Context, group, actor string, in memory.SearchInput) (memory.SearchResult, error) {
	if group != "g" || actor != "u" {
		return memory.SearchResult{}, errors.New("wrong authorization")
	}
	h.calls++
	h.request = in
	return memory.SearchResult{Documents: []memory.Document{{SourceIDs: []string{"old"}}}}, h.err
}
func (h *initialHistory) RecentMessages(_ context.Context, _, _ string, fence int64, _ int) ([]memory.Message, error) {
	h.fence = fence
	return nil, nil
}
func (h *initialHistory) SourceMessages(_ context.Context, _, _ string, fence int64, ids []string) ([]memory.Message, error) {
	if fence != h.fence || len(ids) != 1 || ids[0] != "old" {
		return nil, errors.New("wrong evidence fence")
	}
	return []memory.Message{{ID: "old", Seq: 1, SenderID: "u", Text: "历史中约定周五面试", SentAt: snapshot().Messages[0].SentAt.Add(-time.Hour)}}, nil
}
func initialRequest() aicontext.ContextRequest {
	return aicontext.ContextRequest{Snapshot: snapshot(), TriggerID: "current", Question: "面试是哪天？", Tokens: 16384, MaxSeq: 99}
}

func TestInitialHybridRetrievalPrecedesDirectAnswer(t *testing.T) {
	h := &initialHistory{}
	inject := HybridContextInjector(h, h, nil)
	input, err := inject(context.Background(), initialRequest())
	if err != nil {
		t.Fatal(err)
	}
	if h.calls != 1 || h.request.MaxSeq != 99 || h.request.Limit != 5 || h.request.Kind != "history" || h.request.Query != "面试是哪天？" {
		t.Fatalf("wrong initial retrieval: %+v", h)
	}
	if !input.HistoryScope.HistoricalSearch || input.Request.MessageID != "current" {
		t.Fatal("history or trigger metadata lost")
	}
	model := &modelStub{content: `{"name":"respond","arguments":{"status":"answered","answer":"周五面试。","sourceMessageIds":["old"]}}`}
	registry, err := tools.New(respond.New())
	if err != nil {
		t.Fatal(err)
	}
	service, err := bootstrap.New(model, "test", 16384, registry)
	if err != nil {
		t.Fatal(err)
	}
	run, err := service.Execute(context.Background(), ai.Invocation{AgentID: "main", Mode: "main", Question: initialRequest().Question, Context: input, ContextEndSeq: 99})
	if err != nil || model.calls != 1 || run.Answer == nil || len(run.Sources) != 1 || run.Sources[0].ID != "old" {
		t.Fatalf("direct answer missed retrieved history: %+v %v", run, err)
	}
	if !strings.Contains(model.request.Messages[1].Content, "历史中约定周五面试") {
		t.Fatal("history missing from first model input")
	}

	_, err = HistoryContext(h, h)(context.Background(), ContextRequest{Scope: tools.Scope{Context: input, Question: initialRequest().Question, ContextEndSeq: 99}, Arguments: Arguments{Question: initialRequest().Question}})
	if err != nil || h.calls != 1 {
		t.Fatal("same query retrieved twice", err, h.calls)
	}
}

func TestInitialHybridRetrievalFailsClosedAndPreservesReferences(t *testing.T) {
	h := &initialHistory{}
	inject := HybridContextInjector(h, h, nil)
	request := initialRequest()
	request.MaxSeq = 0
	if _, err := inject(context.Background(), request); err == nil || h.calls != 0 {
		t.Fatal("unfenced retrieval reached store")
	}
	request = initialRequest()
	h.err = errors.New("retrieval offline")
	if _, err := inject(context.Background(), request); !errors.Is(err, h.err) {
		t.Fatal("retrieval failure silently ignored", err)
	}
	h.err = nil
	reference := request.Snapshot.Messages[0]
	reference.ID = "reference"
	reference.Text = "必须保留的引用"
	request.References = []ai.Message{reference}
	input, err := inject(context.Background(), request)
	if err != nil || aicontext.CheckInitialContext(input, request) != nil {
		t.Fatal("pinned request or reference changed", err)
	}
	found := false
	for _, message := range input.Messages {
		if message.ID == reference.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("direct reference lost")
	}
}
