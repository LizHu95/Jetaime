package aicontext_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	aicontext "github.com/LizHu95/Jetaime/services/api/internal/ai/context"
)

func initialContextFixture(t *testing.T) aicontext.ContextRequest {
	t.Helper()
	at := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	input := ai.Context{GroupID: "daily", ActorID: "lin", Users: []ai.User{{ID: "lin", Nickname: "测试用户"}}}
	seed := ai.Message{ID: "seed", GroupID: "daily", SenderID: "lin", SentAt: at, Text: "测试消息"}
	input.Messages = nil
	for i := 0; i < 30; i++ {
		message := seed
		message.ID = fmt.Sprintf("initial-%02d", i)
		message.Text = "近期消息"
		input.Messages = append(input.Messages, message)
	}
	return aicontext.ContextRequest{Snapshot: input, TriggerID: input.Messages[len(input.Messages)-1].ID, Question: "有什么建议？", Tokens: 16384, Now: time.Date(2026, 10, 19, 14, 0, 0, 0, time.UTC)}
}

func TestDefaultContextPinsRequestReferencesAndBoundsRecent(t *testing.T) {
	request := initialContextFixture(t)
	// An old reference is retained even though it is outside the recent window.
	reference := request.Snapshot.Messages[0]
	reference.ID = "old-reference"
	request.References = []ai.Message{reference, reference}
	input, err := aicontext.InjectDefaultContext(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if input.Request == nil || !input.Request.CurrentTime.Equal(request.Now) || input.Request.CurrentTime.Hour() != 22 || input.Request.Timezone != "Asia/Shanghai" || input.Request.MessageID != request.TriggerID {
		t.Fatal("request or time metadata missing", input.Request)
	}
	if len(input.Messages) != aicontext.DefaultRecentMessages+2 || input.Messages[0].ID != reference.ID || input.Messages[len(input.Messages)-1].ID != request.TriggerID || !input.HistoryScope.EarlierMessagesOmitted {
		t.Fatal("required messages or recent limits wrong", input.Messages)
	}
	if len(input.Request.RelatedMessageIDs) != 1 {
		t.Fatal("reference not deduplicated")
	}
	// All timestamps tie: preserve original message order, not reverse read order.
	for i, message := range input.Messages[1:] {
		expected := request.Snapshot.Messages[len(request.Snapshot.Messages)-aicontext.DefaultRecentMessages-1+i]
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
	input, err := aicontext.InjectDefaultContext(context.Background(), request)
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
	if background > aicontext.DefaultRecentBytes || len(input.Messages) <= 1 || len(input.Messages) >= aicontext.DefaultRecentMessages+1 {
		t.Fatal("recent byte budget not enforced", background, len(input.Messages))
	}
}

func TestDefaultContextRequiredEvidenceNeverSilentlyDropped(t *testing.T) {
	request := initialContextFixture(t)
	reference := request.Snapshot.Messages[0]
	reference.ID = "old-reference"
	reference.Text = strings.Repeat("直接引用", 3000)
	request.References = []ai.Message{reference}
	if _, err := aicontext.InjectDefaultContext(context.Background(), request); !errors.Is(err, ai.ErrContext) {
		t.Fatal("oversized direct reference was dropped", err)
	}
	reference.GroupID = "other"
	request.References = []ai.Message{reference}
	if _, err := aicontext.InjectDefaultContext(context.Background(), request); err == nil {
		t.Fatal("foreign reference accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := aicontext.InjectDefaultContext(ctx, request); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation ignored", err)
	}
}

func TestInitialContextChecksInjectedExtensions(t *testing.T) {
	request := initialContextFixture(t)
	input, err := aicontext.InjectDefaultContext(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	input.Messages = input.Messages[:len(input.Messages)-1]
	if err := aicontext.CheckInitialContext(input, request); err == nil {
		t.Fatal("injector dropped current request")
	}
	input, err = aicontext.InjectDefaultContext(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	input.Messages[0].Text = strings.Repeat("扩展内容", 10000)
	if err := aicontext.CheckInitialContext(input, request); !errors.Is(err, ai.ErrContext) {
		t.Fatal("extension bypassed budget", err)
	}
}

func TestOptionalContextDropsBackgroundThenLowestRankedEvidence(t *testing.T) {
	input := ai.Context{Messages: []ai.Message{{ID: "best"}, {ID: "background"}, {ID: "lower"}, {ID: "current"}}, HistoryScope: &ai.HistoryScope{RetrievedMessageIDs: []string{"best", "lower"}}}
	required := map[string]bool{"current": true}
	if got := aicontext.OptionalMessageToDrop(input, required); got != 1 {
		t.Fatalf("background not dropped first: %d", got)
	}
	input.Messages = append(input.Messages[:1], input.Messages[2:]...)
	if got := aicontext.OptionalMessageToDrop(input, required); got != 1 {
		t.Fatalf("best evidence dropped before lower ranked evidence: %d", got)
	}
}
