package mainagent

import (
	"testing"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
)

func TestEvidenceBoundariesRejectAIOnlyAndReminderExpansion(t *testing.T) {
	liz := "liz"
	due := time.Now()
	input := ai.Context{Users: []ai.User{{ID: "liz", Nickname: "Liz"}, {ID: "bot", Nickname: "助手（AI）"}}, Messages: []ai.Message{
		{ID: "request", SenderID: "liz", Text: "提醒我面试 @助手。"},
		{ID: "ai", SenderID: "bot", Text: "明天预约，下午测设备"},
	}}
	got, _ := enforceEvidenceBoundaries(input, []ai.Suggestion{
		{ID: "1", Title: "测设备", AssigneeID: &liz, SourceMessageIDs: []string{"ai"}},
		{ID: "2", Title: "预约时段", AssigneeID: &liz, SourceMessageIDs: []string{"request"}},
		{ID: "3", Title: "提醒面试", AssigneeID: &liz, TimeExpression: "明天", DueAt: &due, SourceMessageIDs: []string{"request", "ai"}},
	})
	if len(got) != 1 || got[0].Title != "提醒面试" || got[0].AssigneeID != nil || got[0].TimeExpression != "" || got[0].DueAt != nil || !sameSources(got[0].SourceMessageIDs, []string{"request"}) {
		t.Fatalf("unsupported action fields retained: %+v", got)
	}
}

func TestEvidenceBoundariesPreserveHumanAcceptanceAndExplicitTime(t *testing.T) {
	b := "b"
	input := ai.Context{Users: []ai.User{{ID: "a", Nickname: "AI研究员"}, {ID: "b", Nickname: "B"}}, Messages: []ai.Message{
		{ID: "request", SenderID: "a", Text: "来叫我起床"},
		{ID: "accept", SenderID: "b", Text: "好"},
		{ID: "remind", SenderID: "a", Text: "请提醒我明天交材料"},
	}}
	got, _ := enforceEvidenceBoundaries(input, []ai.Suggestion{
		{ID: "1", Title: "叫对方起床", AssigneeID: &b, SourceMessageIDs: []string{"request", "accept"}},
		{ID: "2", Title: "提醒交材料", TimeExpression: "明天", SourceMessageIDs: []string{"remind"}},
	})
	if len(got) != 2 || got[0].AssigneeID == nil || *got[0].AssigneeID != b || !sameSources(got[0].SourceMessageIDs, []string{"request", "accept"}) || got[1].TimeExpression != "明天" {
		t.Fatalf("valid human evidence lost: %+v", got)
	}
}
