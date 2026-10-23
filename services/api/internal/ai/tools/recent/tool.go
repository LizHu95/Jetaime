// Package recent supplies original recent messages when a follow-up lost its subject.
package recent

import (
	"context"
	"fmt"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
	"github.com/LizHu95/Jetaime/services/api/internal/chat/history"
)

const Name = "read_recent_messages"
const MessageLimit = 30

type Reader interface {
	RecentMessages(context.Context, string, string, int64, int) ([]history.Message, error)
}
type Output struct {
	OmittedAssistantMessages int          `json:"omittedAssistantMessages"`
	MessageIDs               []string     `json:"messageIds"`
	Coverage                 string       `json:"coverage"`
	Messages                 []ai.Message `json:"-"`
}

// New requires no memory model, embedding, or semantic index. Only original
// messages are read with the same authorization and sequence fence as QA.
func New(reader Reader) *tools.Typed[struct{}, Output] {
	return tools.NewTyped(tools.Metadata{Name: Name, Description: "补读本群截至当前提问的最近30条消息中的真人原文，以及当前显式引用的AI原文。用户说‘我要去吗’‘我考虑的是…’等承接话语，但起始上下文缺少所指事项或原始条件时先用本工具恢复语境，再给建议。返回原文会加入上下文，不能把AI旧回复当用户事实。无参数，只读；相同问题不要重复读取。", Kind: "main", Version: "recent-messages-v1", ReadOnly: true, Idempotent: true, Timeout: 15 * time.Second},
		func(ctx context.Context, scope tools.Scope, _ struct{}) (Output, error) {
			out := Output{MessageIDs: []string{}, Coverage: "recent_window"}
			if reader == nil || scope.ContextEndSeq <= 0 {
				return out, fmt.Errorf("recent messages require a reader and trusted sequence fence")
			}
			messages, err := reader.RecentMessages(ctx, scope.Context.GroupID, scope.Context.ActorID, scope.ContextEndSeq, MessageLimit)
			if err != nil {
				return out, err
			}
			if len(messages) > MessageLimit {
				return out, ai.ErrInvalidDecision
			}
			input := scope.Context
			input.Messages = nil
			for _, message := range messages {
				if message.Seq <= 0 || message.Seq > scope.ContextEndSeq {
					return out, ai.ErrInvalidDecision
				}
				if message.SenderType == "assistant" && (scope.Context.Request == nil || !directlyReferenced(scope.Context, message.ID)) {
					out.OmittedAssistantMessages++
					continue
				}
				original := ai.Message{ID: message.ID, GroupID: scope.Context.GroupID, SenderID: message.SenderID, SenderName: message.SenderName, SenderType: message.SenderType, SentAt: message.SentAt, Text: message.Text}
				input.Messages = append(input.Messages, original)
				out.MessageIDs = append(out.MessageIDs, message.ID)
			}
			if err := ai.ValidateContext(input); err != nil {
				return out, err
			}
			out.Messages = input.Messages
			return out, nil
		}, tools.Options[Output]{Present: func(out Output) tools.Completion {
			return tools.Completion{Sources: out.Messages, HistoryScope: &ai.HistoryScope{Policy: "read-recent-originals-v1", EarlierMessagesOmitted: true}}
		}})
}

func directlyReferenced(input ai.Context, id string) bool {
	for _, message := range input.Messages {
		if message.ID == input.Request.MessageID && message.ReplyToID != nil && *message.ReplyToID == id {
			return true
		}
	}
	return false
}
