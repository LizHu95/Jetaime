package aicontext

import (
	"encoding/json"
	"fmt"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
)

// ExecutionContext selects context after proactive recognition or the reactive
// injector. Preserve server-owned request metadata and pin all required evidence;
// only then fill the latest six optional originals, bounded by bytes as well.
func ExecutionContext(input ai.Context) (ai.Context, error) {
	if input.Request == nil {
		return input, fmt.Errorf("execution request is missing")
	}
	required := map[string]bool{input.Request.MessageID: true}
	for _, id := range input.Request.RelatedMessageIDs {
		required[id] = true
	}
	available := map[string]bool{}
	for _, message := range input.Messages {
		available[message.ID] = true
	}
	for id := range required {
		if !available[id] {
			return input, fmt.Errorf("required execution evidence is missing")
		}
	}
	selected := map[string]bool{}
	for id := range required {
		selected[id] = true
	}
	count, bytes := 0, 0
	for i := len(input.Messages) - 1; i >= 0; i-- {
		message := input.Messages[i]
		if selected[message.ID] {
			continue
		}
		raw, err := json.Marshal(message)
		if err != nil {
			return input, err
		}
		if count == DefaultRecentMessages || bytes+len(raw)+1 > DefaultRecentBytes {
			break
		}
		selected[message.ID] = true
		count++
		bytes += len(raw) + 1
	}
	messages := make([]ai.Message, 0, len(selected))
	for _, message := range input.Messages {
		if selected[message.ID] {
			messages = append(messages, message)
		}
	}
	history := ai.HistoryScope{}
	if input.HistoryScope != nil {
		history = *input.HistoryScope
	}
	history.Policy = "execution-context-v2"
	history.EarlierMessagesOmitted = history.EarlierMessagesOmitted || len(messages) < len(input.Messages)
	input.Messages = messages
	input.HistoryScope = &history
	return input, nil
}
