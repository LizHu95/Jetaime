package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/llm"
)

const DefaultRecentMessages = 12
const DefaultRecentBytes = 4 * 1024

// ContextRequest contains an authorized snapshot and separately loaded direct
// references. Injectors may be wrapped to add future context sources; no model
// call is needed to build the initial context.
type ContextRequest struct {
	Snapshot   Context
	TriggerID  string
	Question   string
	References []Message
	Now        time.Time
	Tokens     int
}

type ContextInjector func(context.Context, ContextRequest) (Context, error)

// InjectDefaultContext pins the current request and direct references, then
// fills a bounded recent window. It neither mutates nor summarizes originals.
func InjectDefaultContext(ctx context.Context, request ContextRequest) (Context, error) {
	if err := ctx.Err(); err != nil {
		return Context{}, err
	}
	input := request.Snapshot
	if err := validateSnapshot(input); err != nil {
		return Context{}, err
	}
	var trigger Message
	for _, message := range input.Messages {
		if message.ID == request.TriggerID {
			trigger = message
			break
		}
	}
	if trigger.ID == "" || trigger.SenderID != input.ActorID {
		return Context{}, fmt.Errorf("current request is missing or unauthorized")
	}
	now := request.Now
	if now.IsZero() {
		now = time.Now()
	}
	zone := time.FixedZone("Asia/Shanghai", 8*3600)
	input.Request = &RequestContext{MessageID: trigger.ID, SenderID: trigger.SenderID, SentAt: trigger.SentAt, CurrentTime: now.In(zone), Timezone: "Asia/Shanghai"}
	input.Tasks = []Task{} // Tasks and long-term memory are loaded on demand later.
	earlier := input.HistoryScope != nil && input.HistoryScope.EarlierMessagesOmitted
	input.HistoryScope = &HistoryScope{Policy: "default-context-v1", EarlierMessagesOmitted: earlier}
	input.Messages = []Message{trigger}
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
		return Context{}, err
	}
	recentBytes, recentCount := 0, 0
	for i := len(request.Snapshot.Messages) - 1; i >= 0; i-- {
		message := request.Snapshot.Messages[i]
		if selected[message.ID] {
			continue
		}
		encoded, err := json.Marshal(message)
		if err != nil {
			return Context{}, err
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
func CheckInitialContext(input Context, request ContextRequest) error {
	if input.GroupID != request.Snapshot.GroupID || input.ActorID != request.Snapshot.ActorID {
		return fmt.Errorf("injected context authorization changed")
	}
	if err := validateSnapshot(input); err != nil {
		return err
	}
	if input.Request == nil || input.Request.MessageID != request.TriggerID || input.Request.SenderID != input.ActorID || input.Request.CurrentTime.IsZero() || input.Request.Timezone == "" {
		return fmt.Errorf("request metadata missing")
	}
	messages := map[string]Message{}
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

func sameContextMessage(left, right Message) bool {
	return left.ID == right.ID && left.GroupID == right.GroupID && left.SenderID == right.SenderID && left.Text == right.Text && left.SentAt.Equal(right.SentAt)
}

func checkInitialBudget(input Context, question string, tokens int) error {
	request, err := mainModelRequest("", input, question, tokens)
	if err != nil {
		return err
	}
	return llm.CheckContextBudget(request, tokens)
}
