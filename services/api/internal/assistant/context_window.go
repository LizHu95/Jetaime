package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/llm"
)

const modelOutputReserve = 2048

func qaSystemPrompt(input Context) string {
	return scopedPrompt(input, qaPrompt, qaSchema)
}

func scopedPrompt(input Context, prompt string, schema json.RawMessage) string {
	if input.HistoryScope != nil {
		prompt += "\n上下文范围：historyScope 描述本次原始消息的覆盖范围。earlierMessagesOmitted=true 时只提供了近期完整消息，未检索更早历史；不得声称已检查全部历史，也不能把本次未见当作从未发生。问题需要更早证据而本次没有时，返回 insufficient_evidence 并说明需要更早原文。"
	}
	return prompt + "\nSchema: " + string(schema)
}

// RecentQAContext keeps a contiguous suffix of complete, chronological messages.
// The newest message is the trigger and must fit; it is never skipped or cut.
// Count the serialized payload, including scope, system prompt and question,
// using the same conservative byte bound as the workflow's final guard.
func RecentQAContext(input Context, question string, tokens int, earlierOmitted bool) (Context, error) {
	return recentContext(input, question, tokens, earlierOmitted, "qa")
}

func RecentRouteContext(input Context, question string, tokens int, earlierOmitted bool) (Context, error) {
	if len(input.Messages) == 0 {
		return input, fmt.Errorf("recent context requires the trigger message")
	}
	input.HistoryScope = &HistoryScope{Policy: "default-context-v1", EarlierMessagesOmitted: earlierOmitted}
	return InjectDefaultContext(context.Background(), ContextRequest{Snapshot: input, TriggerID: input.Messages[len(input.Messages)-1].ID, Question: question, Now: time.Now(), Tokens: tokens})
}

func RecentHelpContext(input Context, question string, tokens int, earlierOmitted bool) (Context, error) {
	return recentContext(input, question, tokens, earlierOmitted, "help")
}

func recentContext(input Context, question string, tokens int, earlierOmitted bool, kind string) (Context, error) {
	if len(input.Messages) == 0 {
		return input, fmt.Errorf("recent context requires the trigger message")
	}
	input.HistoryScope = &HistoryScope{Policy: "recent-messages-v1", EarlierMessagesOmitted: earlierOmitted}
	messages := input.Messages
	// Evaluate both scope values: JSON false is one byte longer than true.
	input.Messages = []Message{}
	input.HistoryScope.EarlierMessagesOmitted = false
	base, err := json.Marshal(struct {
		Context  Context `json:"context"`
		Question string  `json:"question,omitempty"`
	}{input, question})
	if err != nil {
		return input, err
	}
	prompt, schema, reserve := qaSystemPrompt(input), qaSchema, modelOutputReserve
	if kind == "help" {
		prompt = scopedPrompt(input, helpPrompt, qaSchema)
	}
	if kind == "auto" {
		prompt, schema, reserve = mainPrompt+"\nSchema: "+string(mainSchema), mainSchema, modelOutputReserve
	}
	bound := llm.InputTokenUpperBound(ModelRequest{Messages: []ChatMessage{{Role: "system", Content: prompt}, {Role: "user", Content: string(base)}}, Format: schema}) + reserve
	start := len(messages)
	for start > 0 {
		encoded, err := json.Marshal(messages[start-1])
		if err != nil {
			return input, err
		}
		cost := len(encoded)
		if start < len(messages) {
			cost++ // Array separator.
		}
		if bound+cost > tokens {
			break
		}
		bound += cost
		start--
	}
	if start == len(messages) {
		return input, fmt.Errorf("%w: current question and required context do not fit", ErrBudget)
	}
	input.Messages = messages[start:]
	input.HistoryScope.EarlierMessagesOmitted = earlierOmitted || start > 0
	return input, nil
}
