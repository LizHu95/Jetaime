package intervention

import (
	"context"
	"fmt"
	"sort"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/memory"
)

type Searcher interface {
	Search(context.Context, string, string, memory.SearchInput) (memory.SearchResult, error)
}
type MessageReader interface {
	RecentMessages(context.Context, string, string, int64, int) ([]memory.Message, error)
	SourceMessages(context.Context, string, string, int64, []string) ([]memory.Message, error)
}

// HistoryEvidence searches active memories/history once and reads their original
// sources plus neighbours and recent revisions. Summaries never become facts.
// Every read is fenced and authorized by the existing memory services.
func HistoryEvidence(search Searcher, reader MessageReader) ai.InterventionEvidenceProvider {
	return func(ctx context.Context, request ai.InterventionEvidenceRequest) (ai.InterventionEvidence, error) {
		out := ai.InterventionEvidence{Messages: []ai.Message{}, Coverage: "partial"}
		scope := request.Context
		if search == nil || reader == nil || request.ContextEndSeq <= 0 || !validText(request.Question.Topic, 500) || !validText(request.Question.Query, 1500) {
			return out, fmt.Errorf("intervention history requires a provider and trusted scope")
		}
		if err := ai.ValidateContext(scope); err != nil {
			return out, err
		}
		query := request.Question.Topic + "：" + request.Question.Query
		found, err := search.Search(ctx, scope.GroupID, scope.ActorID, memory.SearchInput{Query: query, Limit: 5, MaxSeq: request.ContextEndSeq})
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
		sources, err := reader.SourceMessages(ctx, scope.GroupID, scope.ActorID, request.ContextEndSeq, ids)
		if err != nil {
			return out, err
		}
		if err = ctx.Err(); err != nil {
			return out, err
		}
		recent, err := reader.RecentMessages(ctx, scope.GroupID, scope.ActorID, request.ContextEndSeq, 40)
		if err != nil {
			return out, err
		}
		if len(sources) >= 80 || len(recent) >= 40 {
			out.Truncated = true
		}
		existing := map[string]bool{}
		for _, message := range scope.Messages {
			existing[message.ID] = true
		}
		// Primary retrieved sources first; neighbours and recent originals follow.
		candidates := []memory.Message{}
		for _, id := range ids {
			for _, message := range sources {
				if message.ID == id {
					candidates = append(candidates, message)
				}
			}
		}
		candidates = append(candidates, sources...)
		candidates = append(candidates, recent...)
		used := map[string]bool{}
		for _, message := range candidates {
			if message.Seq <= 0 || message.Seq > request.ContextEndSeq {
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
		sort.SliceStable(out.Messages, func(i, j int) bool { return out.Messages[i].SentAt.Before(out.Messages[j].SentAt) })
		if err := ctx.Err(); err != nil {
			return out, err
		}
		return out, nil
	}
}
