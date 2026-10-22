package chat

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"testing"
)

func TestMentionAndQuestionParsing(t *testing.T) {
	for _, tc := range []struct {
		text, question string
		mention        bool
	}{
		{"@AI我们的预算？", "我们的预算？", true},
		{"@ai 我们的预算？", "我们的预算？", true},
		{"请帮我，@AI查一下", "请帮我，查一下", true},
		{"hello @AIR", "hello @AIR", false},
		{"普通消息", "普通消息", false},
		{"@AI 解释 @AIR", "解释 @AIR", true},
	} {
		if aiMention.MatchString(tc.text) != tc.mention || questionText(tc.text) != tc.question {
			t.Fatalf("mention parsing %q -> %q", tc.text, questionText(tc.text))
		}
	}
}

func TestRetiredExtractModeRejectedBeforeStore(t *testing.T) {
	service := NewService(nil, true)
	_, err := service.Send(context.Background(), uuid.NewString(), uuid.NewString(), SendInput{Content: "@AI 提取待办", ClientID: uuid.NewString(), AgentKind: "extract"})
	if !errors.Is(err, ErrInvalid) {
		t.Fatal("retired mode accepted", err)
	}
}
