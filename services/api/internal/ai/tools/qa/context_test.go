package qa

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
)

func TestEvidenceBudgetPinsCurrentAndReferencesWithoutCuttingOriginals(t *testing.T) {
	input := snapshot()
	current := input.Messages[0]
	reference := current
	reference.ID, reference.Text = "reference", "直接引用的原文"
	reference.SentAt = current.SentAt.Add(-time.Hour)
	input.Request.RelatedMessageIDs = []string{reference.ID}
	input.Messages = []ai.Message{reference}
	for i := 0; i < 100; i++ {
		message := current
		message.ID = fmt.Sprintf("background-%d", i)
		message.SentAt = current.SentAt.Add(time.Duration(i-100) * time.Second)
		message.Text = strings.Repeat("历史内容", 80)
		input.Messages = append(input.Messages, message)
	}
	input.Messages = append(input.Messages, current)
	selected, err := FitContext(input, "之前说什么？", 8192)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected.Messages) >= len(input.Messages) || !selected.HistoryScope.EarlierMessagesOmitted {
		t.Fatal("evidence not bounded")
	}
	originals := map[string]ai.Message{}
	for _, message := range input.Messages {
		originals[message.ID] = message
	}
	kept := map[string]bool{}
	for _, message := range selected.Messages {
		if message != originals[message.ID] {
			t.Fatal("source was cut or changed")
		}
		kept[message.ID] = true
	}
	if !kept[current.ID] || !kept[reference.ID] {
		t.Fatal("required evidence dropped")
	}
	if input.HistoryScope != nil || len(input.Messages) != 102 {
		t.Fatal("source snapshot mutated")
	}
	req, err := request("test", selected, "之前说什么？", 8192)
	if err != nil {
		t.Fatal(err)
	}
	if err = llm.CheckContextBudget(req, 8192); err != nil {
		t.Fatal(err)
	}
}
