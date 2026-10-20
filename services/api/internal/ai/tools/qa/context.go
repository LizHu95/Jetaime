package qa

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/memory"
)

type Searcher interface {
	Search(context.Context, string, string, memory.SearchInput) (memory.SearchResult, error)
}
type MessageReader interface {
	RecentMessages(context.Context, string, string, int64, int) ([]memory.Message, error)
	SourceMessages(context.Context, string, string, int64, []string) ([]memory.Message, error)
}

// HistoryContext returns a function provider, not an Agent or an HTTP client.
// Retrieved summaries only locate source IDs. The QA model sees original text.
func HistoryContext(search Searcher, reader MessageReader) ContextProvider {
	return func(ctx context.Context, r ContextRequest) (ai.Context, error) {
		input := r.Scope.Context
		input.Messages = append([]ai.Message{}, input.Messages...)
		if r.Arguments.Context == "general" {
			return input, nil
		}
		if r.Scope.ContextEndSeq <= 0 {
			return input, fmt.Errorf("QA history requires a trusted sequence fence")
		}
		recent, err := reader.RecentMessages(ctx, input.GroupID, input.ActorID, r.Scope.ContextEndSeq, 40)
		if err != nil {
			return input, err
		}
		searched := false
		extra := []memory.Message{}
		if r.Arguments.Context == "history" || r.Arguments.Context == "mixed" {
			result, e := search.Search(ctx, input.GroupID, input.ActorID, memory.SearchInput{Query: r.Arguments.Question, Limit: 5, Kind: "history", Mode: "hybrid", MaxSeq: r.Scope.ContextEndSeq})
			if e != nil {
				return input, e
			}
			ids := []string{}
			for _, d := range result.Documents {
				ids = append(ids, d.SourceIDs...)
			}
			extra, err = reader.SourceMessages(ctx, input.GroupID, input.ActorID, r.Scope.ContextEndSeq, ids)
			if err != nil {
				return input, err
			}
			searched = true
		}
		seen := map[string]bool{}
		for _, m := range input.Messages {
			seen[m.ID] = true
		}
		for _, list := range [][]memory.Message{recent, extra} {
			for _, m := range list {
				if seen[m.ID] {
					continue
				}
				seen[m.ID] = true
				input.Messages = append(input.Messages, ai.Message{ID: m.ID, GroupID: input.GroupID, SenderID: m.SenderID, SentAt: m.SentAt, Text: m.Text})
			}
		}
		input.Tasks = []ai.Task{}
		input.HistoryScope = &ai.HistoryScope{Policy: "qa-recent-v1", EarlierMessagesOmitted: true, HistoricalSearch: searched}
		if searched {
			input.HistoryScope.Policy = "qa-history-v1"
		}
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
func FitContext(input ai.Context, question, mode string, tokens int) (ai.Context, error) {
	if input.Request == nil {
		req, err := request("", input, question, mode, tokens)
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
	req, err := request("", out, question, mode, tokens)
	if err != nil {
		return out, err
	}
	if err = llm.CheckContextBudget(req, tokens); err != nil {
		return out, err
	}
	for i := len(input.Messages) - 1; i >= 0; i-- {
		m := input.Messages[i]
		if selected[m.ID] {
			continue
		}
		if len(out.Messages) >= 80 {
			break
		}
		out.Messages = append(out.Messages, m)
		req, err = request("", out, question, mode, tokens)
		if err != nil {
			return out, err
		}
		if llm.CheckContextBudget(req, tokens) != nil {
			out.Messages = out.Messages[:len(out.Messages)-1]
			break
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
	req, err = request("", out, question, mode, tokens)
	if err != nil {
		return out, err
	}
	return out, llm.CheckContextBudget(req, tokens)
}

// MarshalArguments is shared by compatibility adapters, never by the main model.
func MarshalArguments(question, mode string) json.RawMessage {
	raw, _ := json.Marshal(Arguments{question, mode})
	return raw
}
