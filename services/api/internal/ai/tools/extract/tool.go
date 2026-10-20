// Package extracttool adapts todo extraction and evidence review to an AI tool.
package extracttool

import (
	"context"
	"fmt"
	"strings"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
	"github.com/google/jsonschema-go/jsonschema"
)

type Tool struct {
	*tools.Typed[Arguments, Output]
	ChatModel    llm.ChatModel
	DefaultModel string
	Tokens       int
}

func New(model llm.ChatModel, name string, tokens int) *Tool {
	t := &Tool{ChatModel: model, DefaultModel: name, Tokens: tokens}
	t.Typed = tools.NewTyped(tools.Metadata{Name: "extract_todos", Description: "仅在用户明确要求提取或整理群聊行动安排时使用。返回待确认建议，不创建任务、提醒或修改状态。", Kind: "extract", Version: "extract-v20-scope-bounded", Model: name, Terminal: true, ReadOnly: true, Idempotent: true}, t.Execute, tools.Options[Output]{Input: func(s *jsonschema.Schema) {
		s.Properties["question"].MinLength = jsonschema.Ptr(1)
		s.Properties["question"].MaxLength = jsonschema.Ptr(3000)
	}, Present: func(o Output) tools.Completion {
		return tools.Completion{Suggestions: o.Suggestions, Sources: o.Sources, HistoryScope: o.HistoryScope}
	}})
	return t
}

type Arguments struct {
	Question string `json:"question"`
}
type Output struct {
	Suggestions  []ai.Suggestion  `json:"suggestions"`
	Sources      []ai.Message     `json:"sources"`
	HistoryScope *ai.HistoryScope `json:"historyScope,omitempty"`
}

func (t *Tool) Execute(ctx context.Context, s tools.Scope, args Arguments) (Output, error) {
	if strings.TrimSpace(args.Question) == "" {
		return Output{}, tools.InvalidArguments("invalid_arguments", "question 不能为空。")
	}
	input := s.Context
	if s.LoadExtraction != nil {
		var err error
		input, err = s.LoadExtraction(ctx)
		if err != nil {
			return Output{}, err
		}
	}
	if input.GroupID != s.Context.GroupID || input.ActorID != s.Context.ActorID {
		return Output{}, fmt.Errorf("extraction context authorization changed")
	}
	inheritRequestContext(&input, s.Context)
	run, err := t.extract(ctx, input, args.Question)
	for _, step := range run.Steps {
		tools.RecordStep(ctx, step)
	}
	if run.ModelIdentity != nil {
		tools.RecordModelIdentity(ctx, *run.ModelIdentity)
	}
	return Output{Suggestions: run.Suggestions, Sources: run.Sources, HistoryScope: run.HistoryScope}, err
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
