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

func TestFitContextPrioritizesSourcesThenNewestBackgroundRegardlessOfAppendOrder(t *testing.T) {
	input := snapshot()
	current := input.Messages[0]
	input.HistoryScope = &ai.HistoryScope{RetrievedMessageIDs: []string{"source"}, HistoricalSearch: true}
	for i := 99; i >= 0; i-- {
		m := current
		m.ID = fmt.Sprintf("background-%d", i)
		m.Text = "背景"
		m.SentAt = current.SentAt.Add(time.Duration(i-101) * time.Minute)
		input.Messages = append(input.Messages, m)
	}
	source := current
	source.ID = "source"
	source.Text = "直接来源"
	source.SentAt = current.SentAt.Add(-time.Hour * 24)
	input.Messages = append(input.Messages, source)
	out, err := FitContext(input, "历史问题", 65536)
	if err != nil || len(out.Messages) != 80 {
		t.Fatal(len(out.Messages), err)
	}
	kept := map[string]bool{}
	for _, m := range out.Messages {
		kept[m.ID] = true
	}
	if !kept["current"] || !kept["source"] || !kept["background-99"] || kept["background-0"] {
		t.Fatal("priority depends on append order", kept)
	}
	if input.Messages[1].ID != "background-99" {
		t.Fatal("mutated original order")
	}
}
