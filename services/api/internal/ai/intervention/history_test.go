package intervention

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/memory"
	"github.com/LizHu95/Jetaime/services/api/internal/chat/history"
)

type historyStub struct {
	t                                     *testing.T
	documents                             []memory.Document
	sources, recent                       []history.Message
	searchCalls, sourceCalls, recentCalls int
	sourceIDs                             []string
	searchErr                             error
	cancel                                context.CancelFunc
}

func (h *historyStub) scope(group, actor string, fence int64) {
	h.t.Helper()
	if group != "g" || actor != "u" || fence != 42 {
		h.t.Fatalf("scope escaped: %s %s %d", group, actor, fence)
	}
}
func (h *historyStub) Search(_ context.Context, group, actor string, in memory.SearchInput) (memory.SearchResult, error) {
	h.scope(group, actor, in.MaxSeq)
	h.searchCalls++
	if in.Limit != 5 || in.Kind != "" || !strings.Contains(in.Query, "本次聚餐") {
		h.t.Fatal("unbounded or unscoped search", in)
	}
	if h.cancel != nil {
		h.cancel()
	}
	return memory.SearchResult{Documents: h.documents}, h.searchErr
}
func (h *historyStub) SourceMessages(_ context.Context, group, actor string, fence int64, ids []string) ([]history.Message, error) {
	h.scope(group, actor, fence)
	h.sourceCalls++
	h.sourceIDs = ids
	return h.sources, nil
}
func (h *historyStub) RecentMessages(_ context.Context, group, actor string, fence int64, limit int) ([]history.Message, error) {
	h.scope(group, actor, fence)
	h.recentCalls++
	if limit != 40 {
		h.t.Fatal("unbounded revision window")
	}
	return h.recent, nil
}
func historyRequest() ai.InterventionEvidenceRequest {
	now := time.Now()
	return ai.InterventionEvidenceRequest{ContextEndSeq: 42, Question: ai.VerificationQuestion{Topic: "本次聚餐", Query: "预算和后续修改"}, Context: ai.Context{GroupID: "g", ActorID: "u", Users: []ai.User{{ID: "u", Nickname: "用户"}}, Messages: []ai.Message{{ID: "current", GroupID: "g", SenderID: "u", SentAt: now, Text: "按之前预算订500元这家"}}}}
}
func TestHistoryEvidenceUsesScopedOriginalsAndRecentRevisions(t *testing.T) {
	now := time.Now().Add(-time.Hour)
	stub := &historyStub{t: t,
		documents: []memory.Document{{Content: "This summary must not become evidence", SourceIDs: []string{"old"}}},
		sources:   []history.Message{{ID: "old", Seq: 3, SenderID: "u", SenderType: "user", Text: "本次预算300", SentAt: now}, {ID: "neighbour", Seq: 4, SenderID: "u", SenderType: "user", Text: "确认", SentAt: now.Add(time.Second)}},
		recent:    []history.Message{{ID: "update", Seq: 40, SenderID: "u", SenderType: "user", Text: "本次预算改成500", SentAt: now.Add(time.Minute)}},
	}
	out, err := NewHistoryEvidenceProvider(stub, stub).Retrieve(context.Background(), historyRequest())
	if err != nil || len(out.Messages) != 3 || out.Coverage != "partial" || stub.searchCalls != 1 || stub.sourceCalls != 1 || stub.recentCalls != 1 || len(stub.sourceIDs) != 1 || stub.sourceIDs[0] != "old" {
		t.Fatal("bad evidence lookup", out, err)
	}
	for _, message := range out.Messages {
		if message.GroupID != "g" || message.SenderType != "user" || strings.Contains(message.Text, "summary") {
			t.Fatal("lost provenance or exposed summary", message)
		}
	}
}
func TestHistoryEvidenceBoundsSourceFanoutAndWholeMessages(t *testing.T) {
	stub := &historyStub{t: t, documents: []memory.Document{{}}}
	for i := 0; i < 60; i++ {
		id := fmt.Sprint("source-", i)
		stub.documents[0].SourceIDs = append(stub.documents[0].SourceIDs, id)
		stub.sources = append(stub.sources, history.Message{ID: id, Seq: 1, SenderID: "u", Text: strings.Repeat("x", 500), SentAt: time.Now().Add(-time.Hour)})
	}
	out, err := NewHistoryEvidenceProvider(stub, stub).Retrieve(context.Background(), historyRequest())
	if err != nil || !out.Truncated || len(stub.sourceIDs) > 20 || len(out.Messages) != ai.MaxInterventionEvidenceMessages {
		t.Fatal("evidence limits failed", err)
	}
	for _, message := range out.Messages {
		if len(message.Text) != 500 {
			t.Fatal("message text truncated")
		}
	}
}
func TestHistoryEvidenceRejectsFutureSequenceAndStopsAfterFailure(t *testing.T) {
	stub := &historyStub{t: t, sources: []history.Message{{ID: "future", Seq: 43}}}
	if _, err := NewHistoryEvidenceProvider(stub, stub).Retrieve(context.Background(), historyRequest()); err == nil {
		t.Fatal("future sequence accepted")
	}
	failure := errors.New("search failed")
	stub = &historyStub{t: t, searchErr: failure}
	if _, err := NewHistoryEvidenceProvider(stub, stub).Retrieve(context.Background(), historyRequest()); !errors.Is(err, failure) || stub.sourceCalls != 0 || stub.recentCalls != 0 {
		t.Fatal("failed search continued", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stub = &historyStub{t: t, cancel: cancel}
	if _, err := NewHistoryEvidenceProvider(stub, stub).Retrieve(ctx, historyRequest()); !errors.Is(err, context.Canceled) || stub.sourceCalls != 0 || stub.recentCalls != 0 {
		t.Fatal("cancelled lookup continued", err)
	}
}

func TestHistoryEvidenceRetainsSourceBeyondFormerByteLimit(t *testing.T) {
	text := strings.Repeat("原文", 4096)
	stub := &historyStub{t: t, documents: []memory.Document{{SourceIDs: []string{"old"}}}, sources: []history.Message{{ID: "old", Seq: 3, SenderID: "u", Text: text, SentAt: time.Now().Add(-time.Hour)}}}
	out, err := NewHistoryEvidenceProvider(stub, stub).Retrieve(context.Background(), historyRequest())
	if err != nil || out.Truncated || len(out.Messages) != 1 || out.Messages[0].Text != text {
		t.Fatal("long source dropped or truncated before model budget check", err)
	}
}
