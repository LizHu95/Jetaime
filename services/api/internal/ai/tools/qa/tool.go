// Package qa owns question answering, evidence selection and source validation.
package qa

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
)

const Name = "answer_question"

type ContextRequest struct {
	Scope     tools.Scope
	Arguments Arguments
	Tokens    int
}
type ContextProvider func(context.Context, ContextRequest) (ai.Context, error)
type Tool struct {
	*tools.Typed[Arguments, Output]
	Model           llm.ChatModel
	ModelName       string
	Tokens          int
	ContextProvider ContextProvider
}

func New(model llm.ChatModel, name string, tokens int, provider ContextProvider) *Tool {
	t := &Tool{Model: model, ModelName: name, Tokens: tokens, ContextProvider: provider}
	t.Typed = tools.NewTyped(tools.Metadata{
		Name:        Name,
		Description: "回答问题、日常建议、写作或澄清。自动混合检索历史并回查原文，同一问题复用已检索上下文；区分群聊事实与一般知识。只读，不创建任务或提醒。",
		Version:     Version,
		Kind:        "main",
		Model:       name,
		Terminal:    true,
		ReadOnly:    true,
		Idempotent:  true,
	}, t.Execute, tools.Options[Output]{
		Input:   inputConstraints,
		Output:  outputConstraints,
		Present: present,
	})
	return t
}

func (t *Tool) Execute(ctx context.Context, scope tools.Scope, args Arguments) (output Output, err error) {
	if strings.TrimSpace(args.Question) == "" || len(args.Question) > 3000 {
		return output, tools.InvalidArguments("invalid_arguments", "question 不能为空且最多3000字节。")
	}
	if err = ai.ValidateContext(scope.Context); err != nil {
		return output, err
	}
	started := time.Now()
	building := true
	defer func() {
		if err != nil && building {
			tools.RecordStep(ctx, ai.Step{Name: "build_context", Status: "failed", StartedAt: started, DurationMS: time.Since(started).Milliseconds(), Error: err.Error()})
		}
	}()
	if provider, ok := t.Model.(interface{ ModelIdentity() llm.Identity }); ok {
		v := provider.ModelIdentity()
		if v.Model != "" && v.Model != t.ModelName {
			return output, fmt.Errorf("QA model is fixed at startup")
		}
		tools.RecordModelIdentity(ctx, v)
	}
	// Required current request/references are checked before any retrieval model I/O.
	baseline, err := FitContext(scope.Context, args.Question, t.Tokens)
	if err != nil {
		return output, err
	}
	provider := t.ContextProvider
	if provider == nil {
		provider = func(_ context.Context, _ ContextRequest) (ai.Context, error) { return baseline, nil }
	}
	providerScope := scope
	providerScope.Context = cloneContext(scope.Context)
	input, err := provider(ctx, ContextRequest{providerScope, args, t.Tokens})
	if err != nil {
		return output, err
	}
	if input.GroupID != scope.Context.GroupID || input.ActorID != scope.Context.ActorID {
		return output, fmt.Errorf("QA context authorization changed")
	}
	if err = ai.ValidateContext(input); err != nil {
		return output, err
	}
	input, err = FitContext(input, args.Question, t.Tokens)
	if err != nil {
		return output, err
	}
	if err = checkPinned(scope.Context, input); err != nil {
		return output, err
	}
	output.HistoryScope = input.HistoryScope
	building = false
	tools.RecordStep(ctx, ai.Step{Name: "build_context", Status: "succeeded", StartedAt: started, DurationMS: time.Since(started).Milliseconds(), Output: input})
	req, err := request(t.ModelName, input, args.Question, t.Tokens)
	if err != nil {
		return output, err
	}
	if err = llm.CheckContextBudget(req, t.Tokens); err != nil {
		tools.RecordStep(ctx, ai.Step{Name: "budget", Status: "failed", StartedAt: time.Now(), Error: err.Error()})
		return output, err
	}
	start := time.Now()
	out, err := t.Model.Call(ctx, req)
	step := ai.Step{Name: "model_call", Status: "succeeded", StartedAt: start, DurationMS: time.Since(start).Milliseconds(), Input: req, Output: map[string]any{"content": out.Content, "promptTokens": out.PromptTokens, "completionTokens": out.CompletionTokens}}
	if err != nil {
		step.Status = "failed"
		step.Error = err.Error()
	}
	tools.RecordStep(ctx, step)
	if err != nil {
		return output, err
	}
	var answer ai.Answer
	err = ai.DecodeJSON([]byte(out.Content), &answer)
	messages := map[string]ai.Message{}
	for _, m := range input.Messages {
		messages[m.ID] = m
	}
	if err == nil && (strings.TrimSpace(answer.Answer) == "" || len(answer.Answer) > 30000 || strings.ContainsRune(answer.Answer, '\x00') || answer.SourceMessageIDs == nil || answer.Status != "answered" && answer.Status != "insufficient_evidence" && answer.Status != "conflicting_evidence") {
		err = fmt.Errorf("invalid QA answer")
	}
	if err == nil {
		err = ai.CheckSources(input.GroupID, answer.SourceMessageIDs, messages, false)
	}
	step = ai.Step{Name: "validate_result", Status: "succeeded", StartedAt: time.Now()}
	if err != nil {
		step.Status = "failed"
		step.Error = err.Error()
	} else {
		output.Answer = answer
		for _, id := range answer.SourceMessageIDs {
			output.Sources = append(output.Sources, messages[id])
		}
		output.Basis = "general"
		if len(answer.SourceMessageIDs) > 0 {
			output.Basis = "history"
		}
	}
	tools.RecordStep(ctx, step)
	return output, err
}
func request(model string, input ai.Context, question string, tokens int) (llm.Request, error) {
	data, err := json.Marshal(struct {
		Context  ai.Context `json:"context"`
		Question string     `json:"question"`
	}{input, question})
	if err != nil {
		return llm.Request{}, err
	}
	omitted := input.HistoryScope != nil && input.HistoryScope.EarlierMessagesOmitted
	return llm.Request{Model: model, Messages: []llm.Message{{Role: "system", Content: system(omitted)}, {Role: "user", Content: string(data)}}, Format: Schema, Options: llm.Options{NumCtx: tokens, NumPredict: 2048}}, nil
}

func (t *Tool) LegacyArguments(s tools.Scope) (json.RawMessage, bool) {
	if s.LegacyMode == "qa" || s.LegacyMode == "help" {
		return MarshalArguments(s.Question), true
	}
	return nil, false
}
