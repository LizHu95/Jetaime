package chat

import "testing"

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
	if extractCommand.MatchString("@AI 什么是 /extract 命令") {
		t.Fatal("mentioning a command invoked extraction")
	}
}
