package mainagent

import (
	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/qa"
	"time"
)

func testContext() ai.Context {
	at := time.Date(2026, 10, 10, 10, 0, 0, 0, time.FixedZone("Asia/Shanghai", 8*3600))
	return ai.Context{GroupID: "daily", ActorID: "lin", Users: []ai.User{{ID: "lin", Nickname: "测试用户 A"}, {ID: "xia", Nickname: "测试用户 B"}}, Tasks: []ai.Task{}, Messages: []ai.Message{
		{ID: "m1", GroupID: "daily", SenderID: "lin", SentAt: at, Text: "测试消息"},
		{ID: "m6", GroupID: "daily", SenderID: "lin", SentAt: at.Add(time.Minute), Text: "我明天负责测试任务"},
	}}
}

func newTestAgent(model llm.ChatModel, name string, tokens int) (*Agent, error) {
	a, err := NewAgent(model, name, tokens)
	if err != nil {
		return nil, err
	}
	a.Tools, err = tools.New(qa.New(model, name, tokens, nil))
	return a, err
}
