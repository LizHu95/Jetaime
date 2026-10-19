package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func initialContextFixture(t *testing.T) ContextRequest {
	t.Helper()
	store, _, _ := setup(t, `{}`)
	input, _ := store.Context("daily", "lin")
	seed := input.Messages[0]
	input.Messages = nil
	for i := 0; i < 30; i++ {
		message := seed
		message.ID = fmt.Sprintf("initial-%02d", i)
		message.Text = "近期消息"
		input.Messages = append(input.Messages, message)
	}
	return ContextRequest{Snapshot: input, TriggerID: input.Messages[len(input.Messages)-1].ID, Question: "有什么建议？", Tokens: 16384, Now: time.Date(2026, 10, 19, 14, 0, 0, 0, time.UTC)}
}

func TestDefaultContextPinsRequestReferencesAndBoundsRecent(t *testing.T) {
	request := initialContextFixture(t)
	// An old reference is retained even though it is outside the recent window.
	reference := request.Snapshot.Messages[0]
	reference.ID = "old-reference"
	request.References = []Message{reference, reference}
	input, err := InjectDefaultContext(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if input.Request == nil || !input.Request.CurrentTime.Equal(request.Now) || input.Request.CurrentTime.Hour() != 22 || input.Request.Timezone != "Asia/Shanghai" || input.Request.MessageID != request.TriggerID {
		t.Fatal("request or time metadata missing", input.Request)
	}
	if len(input.Messages) != DefaultRecentMessages+2 || input.Messages[0].ID != reference.ID || input.Messages[len(input.Messages)-1].ID != request.TriggerID || !input.HistoryScope.EarlierMessagesOmitted {
		t.Fatal("required messages or recent limits wrong", input.Messages)
	}
	if len(input.Request.RelatedMessageIDs) != 1 {
		t.Fatal("reference not deduplicated")
	}
	// All timestamps tie: preserve original message order, not reverse read order.
	for i, message := range input.Messages[1:] {
		expected := request.Snapshot.Messages[len(request.Snapshot.Messages)-DefaultRecentMessages-1+i]
		if message.ID != expected.ID || message.Text != expected.Text {
			t.Fatal("original message changed or reordered")
		}
	}
	if request.Snapshot.Request != nil || request.Snapshot.HistoryScope != nil || len(request.Snapshot.Messages) != 30 {
		t.Fatal("source snapshot was mutated")
	}
}

func TestDefaultContextRecentBytesExcludeRequiredMessages(t *testing.T) {
	request := initialContextFixture(t)
	for i := range request.Snapshot.Messages {
		request.Snapshot.Messages[i].Text = strings.Repeat("近期消息", 60)
	}
	input, err := InjectDefaultContext(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	background := 0
	for _, message := range input.Messages {
		if message.ID == request.TriggerID {
			continue
		}
		encoded, _ := json.Marshal(message)
		background += len(encoded) + 1
	}
	if background > DefaultRecentBytes || len(input.Messages) <= 1 || len(input.Messages) >= DefaultRecentMessages+1 {
		t.Fatal("recent byte budget not enforced", background, len(input.Messages))
	}
}

func TestDefaultContextRequiredEvidenceNeverSilentlyDropped(t *testing.T) {
	request := initialContextFixture(t)
	reference := request.Snapshot.Messages[0]
	reference.ID = "old-reference"
	reference.Text = strings.Repeat("直接引用", 3000)
	request.References = []Message{reference}
	if _, err := InjectDefaultContext(context.Background(), request); !errors.Is(err, ErrBudget) {
		t.Fatal("oversized direct reference was dropped", err)
	}
	reference.GroupID = "other"
	request.References = []Message{reference}
	if _, err := InjectDefaultContext(context.Background(), request); err == nil {
		t.Fatal("foreign reference accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := InjectDefaultContext(ctx, request); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation ignored", err)
	}
}

func TestInitialContextChecksInjectedExtensions(t *testing.T) {
	request := initialContextFixture(t)
	input, err := InjectDefaultContext(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	input.Messages = input.Messages[:len(input.Messages)-1]
	if err := CheckInitialContext(input, request); err == nil {
		t.Fatal("injector dropped current request")
	}
	input, err = InjectDefaultContext(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	input.Messages[0].Text = strings.Repeat("扩展内容", 10000)
	if err := CheckInitialContext(input, request); !errors.Is(err, ErrBudget) {
		t.Fatal("extension bypassed budget", err)
	}
}
