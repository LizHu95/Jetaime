package qa

import (
	"context"
	"fmt"
	"slices"
	"sort"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
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

// HistoryContext returns a function provider, not an Agent or an HTTP client.
// Retrieved summaries only locate source IDs. The QA model sees original text.
func HistoryContext(search Searcher, reader MessageReader) ContextProvider {
	return func(ctx context.Context, r ContextRequest) (ai.Context, error) {
		input := r.Scope.Context
		input.Messages = append([]ai.Message{}, input.Messages...)
		if r.Scope.ContextEndSeq <= 0 {
			return input, fmt.Errorf("QA history requires a trusted sequence fence")
		}

		if input.HistoryScope != nil && input.HistoryScope.HistoricalSearch && r.Arguments.Question == r.Scope.Question {
			return input, nil // Reuse explicitly supplied evidence for the same query.
		}
		// 检索只返回文档，直接来源先于周边原文读取，避免被背景占满额度。
		if search == nil || reader == nil {
			return input, fmt.Errorf("QA history providers unavailable")
		}
		result, err := search.SearchDocuments(ctx, input.GroupID, input.ActorID, memory.SearchInput{Query: r.Arguments.Question, Limit: 5, Kind: "history", MaxSeq: r.Scope.ContextEndSeq})
		if err != nil {
			return input, err
		}
		ids := []string{}
		idSeen := map[string]bool{}
		for _, d := range result.Documents {
			for _, id := range d.SourceIDs {
				if !idSeen[id] && len(ids) < 20 {
					ids = append(ids, id)
					idSeen[id] = true
				}
			}
		}
		extra, err := reader.SourceEvidence(ctx, input.GroupID, input.ActorID, r.Scope.ContextEndSeq, ids)
		if err != nil {
			return input, err
		}
		seen := map[string]bool{}
		exclude := []string{}
		for _, m := range input.Messages {
			if !seen[m.ID] {
				seen[m.ID] = true
				exclude = append(exclude, m.ID)
			}
		}
		appendOriginal := func(m history.Message) error {
			if m.Seq <= 0 || m.Seq > r.Scope.ContextEndSeq {
				return fmt.Errorf("QA evidence escaped sequence fence")
			}
			if !seen[m.ID] {
				seen[m.ID] = true
				exclude = append(exclude, m.ID)
				input.Messages = append(input.Messages, ai.Message{ID: m.ID, GroupID: input.GroupID, SenderID: m.SenderID, SenderName: m.SenderName, SenderType: m.SenderType, SentAt: m.SentAt, Text: m.Text})
			}
			return nil
		}
		for _, list := range [][]history.Message{extra.Sources, extra.Neighbors} {
			for _, m := range list {
				if err := appendOriginal(m); err != nil {
					return input, err
				}
			}
		}
		// 只补记忆游标之后尚未持有的原文，数据库在 LIMIT 前排除重复消息。
		pending, err := search.UnprocessedMessages(ctx, input.GroupID, input.ActorID, memory.SearchInput{MaxSeq: r.Scope.ContextEndSeq}, exclude)
		if err != nil {
			return input, err
		}
		for _, m := range pending.RecentUnprocessed {
			if err := appendOriginal(history.Message{ID: m.ID, Seq: m.Seq, SenderID: m.SenderID, SenderName: m.SenderName, SenderType: "user", SentAt: m.SentAt, Text: m.Text}); err != nil {
				return input, err
			}
		}
		// 可选背景统一按时间排序，不能把拼接顺序误认为新旧顺序。
		sort.SliceStable(input.Messages, func(i, j int) bool { return input.Messages[i].SentAt.Before(input.Messages[j].SentAt) })
		retrievedIDs := ids
		input.Tasks = []ai.Task{}
		input.HistoryScope = &ai.HistoryScope{Policy: "qa-history-v2", EarlierMessagesOmitted: true, HistoricalSearch: true, RetrievedMessageIDs: retrievedIDs}
		return input, nil
	}
}

func checkPinned(before, after ai.Context) error {
	if before.Request == nil {
		return nil
	}
	if after.Request == nil || before.Request.MessageID != after.Request.MessageID || before.Request.SenderID != after.Request.SenderID || !before.Request.SentAt.Equal(after.Request.SentAt) || !before.Request.CurrentTime.Equal(after.Request.CurrentTime) || before.Request.Timezone != after.Request.Timezone || !slices.Equal(before.Request.RelatedMessageIDs, after.Request.RelatedMessageIDs) {
		return fmt.Errorf("QA request metadata changed")
	}
	wanted := map[string]bool{before.Request.MessageID: true}
	for _, id := range before.Request.RelatedMessageIDs {
		wanted[id] = true
	}
	actual := map[string]ai.Message{}
	for _, m := range after.Messages {
		actual[m.ID] = m
	}
	for _, m := range before.Messages {
		if wanted[m.ID] {
			got, ok := actual[m.ID]
			if !ok || got.GroupID != m.GroupID || got.SenderID != m.SenderID || got.Text != m.Text || !got.SentAt.Equal(m.SentAt) {
				return fmt.Errorf("QA dropped or changed required evidence")
			}
		}
	}
	return nil
}

// Providers receive their own copy so in-place edits cannot change the trusted
// request against which their result is checked.
func cloneContext(input ai.Context) ai.Context {
	input.Messages = slices.Clone(input.Messages)
	input.Users = slices.Clone(input.Users)
	input.Tasks = slices.Clone(input.Tasks)
	if input.Request != nil {
		request := *input.Request
		request.RelatedMessageIDs = slices.Clone(request.RelatedMessageIDs)
		input.Request = &request
	}
	if input.HistoryScope != nil {
		scope := *input.HistoryScope
		input.HistoryScope = &scope
	}
	return input
}

// FitContext pins the current request and direct references, then includes
// whole evidence messages within the final QA request budget. No text is cut.
func FitContext(input ai.Context, question string, tokens int) (ai.Context, error) {
	if input.Request == nil {
		req, err := request("", input, question, tokens)
		if err != nil {
			return input, err
		}
		return input, llm.CheckContextBudget(req, tokens)
	}
	out := input
	out.Messages = []ai.Message{}
	out.Tasks = []ai.Task{}
	required := map[string]bool{input.Request.MessageID: true}
	for _, id := range input.Request.RelatedMessageIDs {
		required[id] = true
	}
	selected := map[string]bool{}
	for _, m := range input.Messages {
		if required[m.ID] {
			out.Messages = append(out.Messages, m)
			selected[m.ID] = true
		}
	}
	if len(selected) != len(required) {
		return out, fmt.Errorf("QA required evidence missing")
	}
	scope := ai.HistoryScope{Policy: "qa-window-v1"}
	if input.HistoryScope != nil {
		scope = *input.HistoryScope
	}
	earlier := scope.EarlierMessagesOmitted
	scope.EarlierMessagesOmitted = earlier || len(out.Messages) < len(input.Messages)
	out.HistoryScope = &scope
	req, err := request("", out, question, tokens)
	if err != nil {
		return out, err
	}
	if err = llm.CheckContextBudget(req, tokens); err != nil {
		return out, err
	}
	byID := map[string]ai.Message{}
	for _, m := range input.Messages {
		byID[m.ID] = m
	}
	candidates := []ai.Message{}
	queued := map[string]bool{}
	// Ranked retrieval evidence takes priority over recent optional background.
	for _, id := range scope.RetrievedMessageIDs {
		if m, ok := byID[id]; ok && !selected[id] && !queued[id] {
			candidates = append(candidates, m)
			queued[id] = true
		}
	}
	background := slices.Clone(input.Messages)
	sort.SliceStable(background, func(i, j int) bool { return background[i].SentAt.Before(background[j].SentAt) })
	for i := len(background) - 1; i >= 0; i-- {
		m := background[i]
		if !selected[m.ID] && !queued[m.ID] {
			candidates = append(candidates, m)
			queued[m.ID] = true
		}
	}
	for _, m := range candidates {
		if len(out.Messages) >= 80 {
			break
		}
		out.Messages = append(out.Messages, m)
		req, err = request("", out, question, tokens)
		if err != nil {
			return out, err
		}
		if llm.CheckContextBudget(req, tokens) != nil {
			out.Messages = out.Messages[:len(out.Messages)-1]
			continue
		}
		selected[m.ID] = true
	}
	scope.EarlierMessagesOmitted = earlier || len(out.Messages) < len(input.Messages)
	sort.SliceStable(out.Messages, func(i, j int) bool {
		if out.Messages[i].ID == input.Request.MessageID {
			return false
		}
		if out.Messages[j].ID == input.Request.MessageID {
			return true
		}
		return out.Messages[i].SentAt.Before(out.Messages[j].SentAt)
	})
	req, err = request("", out, question, tokens)
	if err != nil {
		return out, err
	}
	return out, llm.CheckContextBudget(req, tokens)
}
