package aicontext

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/prompts"
	"github.com/LizHu95/Jetaime/services/api/internal/llm"
)

// RecentQAContext keeps a contiguous suffix of complete, chronological messages.
// The newest message is the trigger and must fit; it is never skipped or cut.
// Count the serialized payload, including scope, system prompt and question,
// using the same conservative byte bound as the workflow's final guard.
func RecentQAContext(input ai.Context, question string, tokens int, earlierOmitted bool) (ai.Context, error) {
	return recentContext(input, question, tokens, earlierOmitted, "qa")
}

func RecentRouteContext(input ai.Context, question string, tokens int, earlierOmitted bool) (ai.Context, error) {
	if len(input.Messages) == 0 {
		return input, fmt.Errorf("recent context requires the trigger message")
	}
	input.HistoryScope = &ai.HistoryScope{Policy: "default-context-v1", EarlierMessagesOmitted: earlierOmitted}
	return InjectDefaultContext(context.Background(), ContextRequest{Snapshot: input, TriggerID: input.Messages[len(input.Messages)-1].ID, Question: question, Now: time.Now(), Tokens: tokens})
}

func RecentHelpContext(input ai.Context, question string, tokens int, earlierOmitted bool) (ai.Context, error) {
	return recentContext(input, question, tokens, earlierOmitted, "help")
}

func recentContext(input ai.Context, question string, tokens int, earlierOmitted bool, kind string) (ai.Context, error) {
	if len(input.Messages) == 0 {
		return input, fmt.Errorf("recent context requires the trigger message")
	}
	input.HistoryScope = &ai.HistoryScope{Policy: "recent-messages-v1", EarlierMessagesOmitted: earlierOmitted}
	messages := input.Messages
	// Evaluate both scope values: JSON false is one byte longer than true.
	input.Messages = []ai.Message{}
	input.HistoryScope.EarlierMessagesOmitted = false
	base, err := json.Marshal(struct {
		Context  ai.Context `json:"context"`
		Question string     `json:"question,omitempty"`
	}{input, question})
	if err != nil {
		return input, err
	}
	prompt, schema, reserve := prompts.QASystem(input), prompts.QASchema, prompts.OutputReserve
	if kind == "help" {
		prompt = prompts.Scoped(input, prompts.Help, prompts.QASchema)
	}
	bound := llm.InputTokenUpperBound(llm.Request{Messages: []llm.Message{{Role: "system", Content: prompt}, {Role: "user", Content: string(base)}}, Format: schema}) + reserve
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

const DefaultRecentMessages = 12
const DefaultRecentBytes = 4 * 1024

// ContextRequest contains an authorized snapshot and separately loaded direct
// references. Injectors may be wrapped to add future context sources; no model
// call is needed to build the initial context.
type ContextRequest struct {
	Snapshot   ai.Context
	TriggerID  string
	Question   string
	References []ai.Message
	Now        time.Time
	Tokens     int
}

type ContextInjector func(context.Context, ContextRequest) (ai.Context, error)

// InjectDefaultContext pins the current request and direct references, then
// fills a bounded recent window. It neither mutates nor summarizes originals.
func InjectDefaultContext(ctx context.Context, request ContextRequest) (ai.Context, error) {
	if err := ctx.Err(); err != nil {
		return ai.Context{}, err
	}
	input := request.Snapshot
	if err := ai.ValidateContext(input); err != nil {
		return ai.Context{}, err
	}
	var trigger ai.Message
	for _, message := range input.Messages {
		if message.ID == request.TriggerID {
			trigger = message
			break
		}
	}
	if trigger.ID == "" || trigger.SenderID != input.ActorID {
		return ai.Context{}, fmt.Errorf("current request is missing or unauthorized")
	}
	now := request.Now
	if now.IsZero() {
		now = time.Now()
	}
	zone := time.FixedZone("Asia/Shanghai", 8*3600)
	input.Request = &ai.RequestContext{MessageID: trigger.ID, SenderID: trigger.SenderID, SentAt: trigger.SentAt, CurrentTime: now.In(zone), Timezone: "Asia/Shanghai"}
	input.Tasks = []ai.Task{} // Tasks and long-term memory are loaded on demand later.
	earlier := input.HistoryScope != nil && input.HistoryScope.EarlierMessagesOmitted
	input.HistoryScope = &ai.HistoryScope{Policy: "default-context-v1", EarlierMessagesOmitted: earlier}
	input.Messages = []ai.Message{trigger}
	selected := map[string]bool{trigger.ID: true}
	for _, reference := range request.References {
		if selected[reference.ID] {
			continue
		}
		input.Messages = append(input.Messages, reference)
		input.Request.RelatedMessageIDs = append(input.Request.RelatedMessageIDs, reference.ID)
		selected[reference.ID] = true
	}
	if err := CheckInitialContext(input, request); err != nil {
		return ai.Context{}, err
	}
	recentBytes, recentCount := 0, 0
	for i := len(request.Snapshot.Messages) - 1; i >= 0; i-- {
		message := request.Snapshot.Messages[i]
		if selected[message.ID] {
			continue
		}
		encoded, err := json.Marshal(message)
		if err != nil {
			return ai.Context{}, err
		}
		cost := len(encoded) + 1
		if recentCount == DefaultRecentMessages || recentBytes+cost > DefaultRecentBytes {
			break
		}
		input.Messages = append(input.Messages, message)
		if err := checkInitialBudget(input, request.Question, request.Tokens); err != nil {
			input.Messages = input.Messages[:len(input.Messages)-1]
			break
		}
		selected[message.ID] = true
		recentBytes += cost
		recentCount++
	}
	for _, message := range request.Snapshot.Messages {
		if !selected[message.ID] {
			input.HistoryScope.EarlierMessagesOmitted = true
			break
		}
	}
	// Preserve source order even when database timestamps tie. References outside
	// the loaded recent window precede that window; the current request is last.
	positions := map[string]int{}
	for index, message := range request.Snapshot.Messages {
		positions[message.ID] = index
	}
	sort.SliceStable(input.Messages, func(i, j int) bool {
		if input.Messages[i].ID == trigger.ID {
			return false
		}
		if input.Messages[j].ID == trigger.ID {
			return true
		}
		left, leftKnown := positions[input.Messages[i].ID]
		right, rightKnown := positions[input.Messages[j].ID]
		if leftKnown && rightKnown {
			return left < right
		}
		if leftKnown != rightKnown {
			return !leftKnown
		}
		return input.Messages[i].SentAt.Before(input.Messages[j].SentAt)
	})
	return input, CheckInitialContext(input, request)
}

// CheckInitialContext remains outside the injectable function, so extensions
// cannot accidentally bypass required messages, authorization or the budget.
func CheckInitialContext(input ai.Context, request ContextRequest) error {
	if input.GroupID != request.Snapshot.GroupID || input.ActorID != request.Snapshot.ActorID {
		return fmt.Errorf("injected context authorization changed")
	}
	if err := ai.ValidateContext(input); err != nil {
		return err
	}
	if input.Request == nil || input.Request.MessageID != request.TriggerID || input.Request.SenderID != input.ActorID || input.Request.CurrentTime.IsZero() || input.Request.Timezone == "" {
		return fmt.Errorf("request metadata missing")
	}
	messages := map[string]ai.Message{}
	for _, message := range input.Messages {
		messages[message.ID] = message
	}
	found := false
	for _, message := range request.Snapshot.Messages {
		if message.ID != request.TriggerID {
			continue
		}
		found = true
		if !sameContextMessage(messages[message.ID], message) || !input.Request.SentAt.Equal(message.SentAt) {
			return fmt.Errorf("current request was dropped or changed")
		}
	}
	if !found {
		return fmt.Errorf("current request is missing")
	}
	for _, message := range request.References {
		if !sameContextMessage(messages[message.ID], message) {
			return fmt.Errorf("direct reference was dropped or changed")
		}
	}
	return checkInitialBudget(input, request.Question, request.Tokens)
}

func sameContextMessage(left, right ai.Message) bool {
	return left.ID == right.ID && left.GroupID == right.GroupID && left.SenderID == right.SenderID && left.Text == right.Text && left.SentAt.Equal(right.SentAt)
}

func checkInitialBudget(input ai.Context, question string, tokens int) error {
	request, err := prompts.MainRequest("", input, question, tokens)
	if err != nil {
		return err
	}
	return llm.CheckContextBudget(request, tokens)
}
