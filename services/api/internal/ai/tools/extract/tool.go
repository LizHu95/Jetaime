// Package extracttool adapts todo extraction and evidence review to an AI tool.
package extracttool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
)

type Tool struct {
	ChatModel    llm.ChatModel
	DefaultModel string
	Tokens       int
}

func New(model llm.ChatModel, name string, tokens int) *Tool {
	return &Tool{ChatModel: model, DefaultModel: name, Tokens: tokens}
}

func (*Tool) Definition() tools.Definition {
	return tools.Definition{Name: "extract_todos", Description: "仅在用户明确要求提取或整理群聊行动安排时使用。返回待确认建议，不创建任务、提醒或修改状态。", Parameters: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["question"],"properties":{"question":{"type":"string"}}}`)}
}

func (t *Tool) Invoke(ctx context.Context, s tools.Scope, raw json.RawMessage) (tools.Result, error) {
	var args struct {
		Question string `json:"question"`
	}
	if ai.DecodeJSON(raw, &args) != nil || strings.TrimSpace(args.Question) == "" || len(args.Question) > 3000 {
		return tools.Result{}, ai.ErrInvalidDecision
	}
	input := s.Context
	if s.LoadExtraction != nil {
		var err error
		input, err = s.LoadExtraction(ctx)
		if err != nil {
			return tools.Result{}, err
		}
	}
	if input.GroupID != s.Context.GroupID || input.ActorID != s.Context.ActorID {
		return tools.Result{}, fmt.Errorf("extraction context authorization changed")
	}
	inheritRequestContext(&input, s.Context)
	run, err := t.extract(ctx, input, args.Question)
	return tools.Result{Run: run, Terminal: true}, err
}

func inheritRequestContext(child *ai.Context, parent ai.Context) {
	if parent.Request == nil {
		return
	}
	child.Request = parent.Request
	present := map[string]bool{}
	for _, m := range child.Messages {
		present[m.ID] = true
	}
	needed := map[string]bool{parent.Request.MessageID: true}
	for _, id := range parent.Request.RelatedMessageIDs {
		needed[id] = true
	}
	missing := []ai.Message{}
	for _, m := range parent.Messages {
		if needed[m.ID] && !present[m.ID] {
			missing = append(missing, m)
			present[m.ID] = true
		}
	}
	child.Messages = append(missing, child.Messages...)
}
