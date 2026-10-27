package intervention

import (
	"context"
	"fmt"
	"sort"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/memory"
	"github.com/LizHu95/Jetaime/services/api/internal/chat/history"
)

type Searcher interface {
	SearchDocuments(context.Context, string, string, memory.SearchInput) (memory.SearchResult, error)
	UnprocessedMessages(context.Context, string, string, memory.SearchInput, []string) (memory.SearchResult, error)
}
type MessageReader interface {
	SourceEvidence(context.Context, string, string, int64, []string) (history.SourceEvidence, error)
}

// HistoryEvidenceProvider retrieves historical originals using memory search and chat reads.
type HistoryEvidenceProvider struct {
	search Searcher      // 按话题和核验问题检索相关记忆/历史文档，取得来源消息 ID；生产环境注入 memory.Service。
	reader MessageReader // 回查直接来源及相邻聊天原文；生产环境注入 chat.Service。
}

var _ ai.InterventionEvidenceProvider = (*HistoryEvidenceProvider)(nil)

func NewHistoryEvidenceProvider(search Searcher, reader MessageReader) *HistoryEvidenceProvider {
	return &HistoryEvidenceProvider{search: search, reader: reader}
}

// Retrieve searches documents once, reads sources before neighbours, reuses the
// recognition window and supplements only pending originals absent from that window.
// Summaries locate evidence; only original messages are returned as facts.
func (p *HistoryEvidenceProvider) Retrieve(ctx context.Context, request ai.InterventionEvidenceRequest) (ai.InterventionEvidence, error) {
	out := ai.InterventionEvidence{Messages: []ai.Message{}, Coverage: "partial"}
	scope := request.Context
	if p.search == nil || p.reader == nil || len(request.Window) > ai.InterventionMessageLimit || request.ContextEndSeq <= 0 || !validText(request.Question.Topic, 500) || !validText(request.Question.Query, 1500) {
		return out, fmt.Errorf("intervention history requires a provider and trusted scope")
	}
	if err := ai.ValidateContext(scope); err != nil {
		return out, err
	}
	query := request.Question.Topic + "：" + request.Question.Query
	found, err := p.search.SearchDocuments(ctx, scope.GroupID, scope.ActorID, memory.SearchInput{Query: query, Limit: 5, MaxSeq: request.ContextEndSeq})
	if err != nil {
		return out, err
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	ids, seen := []string{}, map[string]bool{}
	// Bound source fan-out independently of document sizes and provider behavior.
	for i, document := range found.Documents {
		if i >= 5 {
			out.Truncated = true
			break
		}
		for _, id := range document.SourceIDs {
			if seen[id] {
				continue
			}
			if len(ids) == 20 {
				out.Truncated = true
				continue
			}
			ids, seen[id] = append(ids, id), true
		}
	}
	sources, err := p.reader.SourceEvidence(ctx, scope.GroupID, scope.ActorID, request.ContextEndSeq, ids)
	if err != nil {
		return out, err
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	existing := map[string]bool{}
	exclude := []string{}
	for _, m := range scope.Messages {
		existing[m.ID] = true
		exclude = append(exclude, m.ID)
	}
	// Reuse the initial window even when Discover selected a smaller topic.
	window := []history.Message{}
	for _, m := range request.Window {
		if m.GroupID != scope.GroupID || (scope.Request != nil && m.SentAt.After(scope.Request.SentAt)) {
			return out, fmt.Errorf("invalid recognition window")
		}
		if !existing[m.ID] {
			exclude = append(exclude, m.ID)
		}
		window = append(window, history.Message{ID: m.ID, SenderID: m.SenderID, SenderName: m.SenderName, SenderType: m.SenderType, Text: m.Text, SentAt: m.SentAt})
	}
	// Exclude source reads too, so they cannot consume the pending-message quota.
	for _, m := range append(append([]history.Message{}, sources.Sources...), sources.Neighbors...) {
		if m.Seq <= 0 || m.Seq > request.ContextEndSeq {
			return out, fmt.Errorf("intervention evidence escaped sequence fence")
		}
		exclude = append(exclude, m.ID)
	}
	pending, err := p.search.UnprocessedMessages(ctx, scope.GroupID, scope.ActorID, memory.SearchInput{MaxSeq: request.ContextEndSeq}, exclude)
	if err != nil {
		return out, err
	}
	out.Truncated = out.Truncated || sources.Truncated || pending.RecentOmitted
	// Direct originals get the evidence budget first, then neighbours, reused
	// recent originals and pending messages absent from the initial window.
	candidates := append([]history.Message{}, sources.Sources...)
	candidates = append(candidates, sources.Neighbors...)
	candidates = append(candidates, window...)
	for _, m := range pending.RecentUnprocessed {
		candidates = append(candidates, history.Message{ID: m.ID, Seq: m.Seq, SenderID: m.SenderID, SenderName: m.SenderName, SenderType: "user", Text: m.Text, SentAt: m.SentAt})
	}
	windowIDs := map[string]bool{}
	for _, m := range request.Window {
		windowIDs[m.ID] = true
	}
	used := map[string]bool{}
	for _, message := range candidates {
		if !windowIDs[message.ID] && (message.Seq <= 0 || message.Seq > request.ContextEndSeq) {
			return out, fmt.Errorf("intervention evidence escaped sequence fence")
		}
		if existing[message.ID] || used[message.ID] {
			continue
		}
		used[message.ID] = true
		original := ai.Message{ID: message.ID, GroupID: scope.GroupID, SenderID: message.SenderID, SenderType: message.SenderType, SenderName: message.SenderName, Text: message.Text, SentAt: message.SentAt}
		if len(out.Messages) >= ai.MaxInterventionEvidenceMessages {
			out.Truncated = true
			continue // Keep complete messages; never cut a source's text.
		}
		out.Messages = append(out.Messages, original)
	}
	// Preserve query/window order when timestamps tie; the trigger remains in conversation.
	sort.SliceStable(out.Messages, func(i, j int) bool { return out.Messages[i].SentAt.Before(out.Messages[j].SentAt) })
	if err := ctx.Err(); err != nil {
		return out, err
	}
	return out, nil
}
