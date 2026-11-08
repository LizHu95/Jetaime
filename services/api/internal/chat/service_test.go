package chat

import (
	"encoding/json"
	"strings"
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

func TestChatAPIOmitsAgentKind(t *testing.T) {
	for _, value := range []any{Message{AgentKind: "main"}, Run{Kind: "main"}} {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "agentKind") {
			t.Fatalf("internal kind exposed: %s", data)
		}
	}
	var input SendInput
	decoder := json.NewDecoder(strings.NewReader(`{"content":"hello","agentKind":"qa"}`))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err == nil {
		t.Fatal("removed agentKind request field accepted")
	}
}
