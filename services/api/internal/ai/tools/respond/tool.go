// Package respond validates a final answer already written by the main model.
package respond

import (
	"context"
	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
	"github.com/google/jsonschema-go/jsonschema"
	"strings"
	"unicode/utf8"
)

type Arguments struct {
	Status           string      `json:"status"`
	Answer           string      `json:"answer"`
	SourceMessageIDs []string    `json:"sourceMessageIds"`
	Cards            []Selection `json:"cards,omitempty" jsonschema:"可选，从本轮工具结果cardRefs中选择最多5个引用；只填写ref和推荐理由，不填写事实字段"`
}
type Selection struct {
	Ref    string `json:"ref"`
	Reason string `json:"reason" jsonschema:"结合本次需求的推荐理由，最多160字；不编造工具未返回的口味、价格或营业承诺"`
}

type Output struct {
	Cards        []ai.Card        `json:"-"`
	Status       string           `json:"status"`
	Answer       string           `json:"answer"`
	SourceIDs    []string         `json:"sourceMessageIds"`
	Sources      []ai.Message     `json:"-"`
	HistoryScope *ai.HistoryScope `json:"-"`
}

func New() *tools.Typed[Arguments, Output] {
	return tools.NewTyped(tools.Metadata{Name: "respond", Description: "直接返回你写好的最终回答，不再调用模型；用于无需检索或外部操作的问题。", Kind: "main", Terminal: true, ReadOnly: true, Idempotent: true}, execute, tools.Options[Output]{Input: func(s *jsonschema.Schema) {
		s.Properties["status"].Enum = []any{"answered", "insufficient_evidence", "conflicting_evidence"}
		s.Properties["answer"].MinLength = jsonschema.Ptr(1)
		s.Properties["answer"].MaxLength = jsonschema.Ptr(30000)
		ids := s.Properties["sourceMessageIds"]
		ids.Type = "array"
		ids.Types = nil
		ids.UniqueItems = true
	}, Present: func(out Output) tools.Completion {
		basis := "general"
		if len(out.SourceIDs) > 0 {
			basis = "history"
		}
		return tools.Completion{Answer: &ai.Answer{Status: out.Status, Answer: out.Answer, SourceMessageIDs: out.SourceIDs}, Basis: basis, Sources: out.Sources, HistoryScope: out.HistoryScope, Cards: out.Cards}
	}})
}

func execute(_ context.Context, scope tools.Scope, answer Arguments) (Output, error) {
	if strings.TrimSpace(answer.Answer) == "" || len(answer.Answer) > 30000 || strings.ContainsRune(answer.Answer, '\x00') {
		return Output{}, tools.InvalidArguments("invalid_answer", "回答必须是非空、有效且不超过30000字节的文本。")
	}
	messages := map[string]ai.Message{}
	for _, message := range scope.Context.Messages {
		messages[message.ID] = message
	}
	if err := ai.CheckSources(scope.Context.GroupID, answer.SourceMessageIDs, messages, false); err != nil {
		return Output{}, tools.InvalidArguments("invalid_answer_sources", "来源只能引用本次上下文中本群的消息ID，不能伪造或重复引用。需要更多历史时调用 answer_question。")
	}
	out := Output{Status: answer.Status, Answer: answer.Answer, SourceIDs: answer.SourceMessageIDs}
	if len(answer.Cards) > ai.MaxCards || len(answer.Cards) > 0 && answer.Status != "answered" {
		return Output{}, tools.InvalidArguments("invalid_cards", "卡片仅用于有依据的回答，最多选择5个本轮cardRefs引用。")
	}
	selected := map[string]bool{}
	for _, selection := range answer.Cards {
		if scope.ExcludedCards[selection.Ref] {
			return Output{}, tools.InvalidArguments("previously_recommended_place", "本轮要求换一批，不能重复展示近期已推荐地点；请选择新的cardRefs，候选不足时如实说明。")
		}
		card, ok := scope.Cards[selection.Ref]
		if !ok || selected[selection.Ref] || utf8.RuneCountInString(selection.Reason) > ai.MaxCardReason || strings.ContainsRune(selection.Reason, '\x00') {
			return Output{}, tools.InvalidArguments("invalid_card_reference", "卡片只能选择本轮成功工具结果中的cardRefs，不得编造或重复引用；理由不超过160字。")
		}
		card.Reason = strings.TrimSpace(selection.Reason)
		out.Cards = append(out.Cards, card)
		selected[selection.Ref] = true
	}
	if err := ai.ValidateCards(out.Cards); err != nil {
		return Output{}, tools.InvalidArguments("invalid_cards", "卡片超出有效内容范围，请减少选择并缩短理由。")
	}
	if len(answer.SourceMessageIDs) > 0 {
		out.HistoryScope = scope.Context.HistoryScope
	}
	for _, id := range answer.SourceMessageIDs {
		out.Sources = append(out.Sources, messages[id])
	}
	return out, nil
}
