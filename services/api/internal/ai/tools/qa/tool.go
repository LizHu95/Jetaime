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
	"github.com/google/uuid"
)

const Name = "answer_question"

type Arguments struct {
	Question string `json:"question"`
	Context  string `json:"context"`
}
type ContextRequest struct {
	Scope     tools.Scope
	Arguments Arguments
	Tokens    int
}
type ContextProvider func(context.Context, ContextRequest) (ai.Context, error)
type Tool struct {
	Model           llm.ChatModel
	ModelName       string
	Tokens          int
	ContextProvider ContextProvider
}

func New(model llm.ChatModel, name string, tokens int, provider ContextProvider) *Tool {
	return &Tool{model, name, tokens, provider}
}
func (t *Tool) Definition() tools.Definition {
	return tools.Definition{Name: Name, Description: "回答问题、日常建议、写作或澄清；general使用一般知识，recent理解近期对话，history查询群聊历史事实，mixed结合群聊事实与一般建议。必要时内部检索历史并回查原文。只读，不创建任务或提醒。", Parameters: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["question","context"],"properties":{"question":{"type":"string","minLength":1,"maxLength":3000},"context":{"type":"string","enum":["general","recent","history","mixed"]}}}`)}
}
func (t *Tool) Invoke(ctx context.Context, scope tools.Scope, raw json.RawMessage) (result tools.Result, err error) {
	var args Arguments
	if ai.DecodeJSON(raw, &args) != nil || strings.TrimSpace(args.Question) == "" || len(args.Question) > 3000 {
		return result, ai.ErrInvalidDecision
	}
	switch args.Context {
	case "general", "recent", "history", "mixed":
	default:
		return result, ai.ErrInvalidDecision
	}
	if err = ai.ValidateContext(scope.Context); err != nil {
		return result, err
	}
	started := time.Now()
	building := true
	kind := "main"
	if scope.LegacyMode == "qa" || scope.LegacyMode == "help" {
		kind = scope.LegacyMode
	}
	run := ai.Run{ID: "run-" + uuid.NewString(), Kind: kind, GroupID: scope.Context.GroupID, ActorID: scope.Context.ActorID, Question: args.Question, Model: t.ModelName, PromptVersion: Version, StartedAt: started, Status: "running", Steps: []ai.Step{}}
	defer func() {
		run.DurationMS = time.Since(started).Milliseconds()
		run.Status = "succeeded"
		if err != nil {
			run.Status = "failed"
			run.Error = err.Error()
			if building {
				run.Steps = append(run.Steps, ai.Step{Name: "build_context", Status: "failed", StartedAt: started, DurationMS: time.Since(started).Milliseconds(), Error: err.Error()})
			}
		}
		result.Run = run
		result.Terminal = true
	}()
	if provider, ok := t.Model.(interface{ ModelIdentity() llm.Identity }); ok {
		v := provider.ModelIdentity()
		if v.Model != "" && v.Model != t.ModelName {
			return result, fmt.Errorf("QA model is fixed at startup")
		}
		run.ModelIdentity = &v
	}
	// Required current request/references are checked before any retrieval model I/O.
	baseline, err := FitContext(scope.Context, args.Question, args.Context, t.Tokens)
	if err != nil {
		return result, err
	}
	provider := t.ContextProvider
	if provider == nil {
		provider = func(_ context.Context, _ ContextRequest) (ai.Context, error) { return baseline, nil }
	}
	providerScope := scope
	providerScope.Context = cloneContext(scope.Context)
	input, err := provider(ctx, ContextRequest{providerScope, args, t.Tokens})
	if err != nil {
		return result, err
	}
	if input.GroupID != scope.Context.GroupID || input.ActorID != scope.Context.ActorID {
		return result, fmt.Errorf("QA context authorization changed")
	}
	if err = ai.ValidateContext(input); err != nil {
		return result, err
	}
	input, err = FitContext(input, args.Question, args.Context, t.Tokens)
	if err != nil {
		return result, err
	}
	if err = checkPinned(scope.Context, input); err != nil {
		return result, err
	}
	run.HistoryScope = input.HistoryScope
	building = false
	run.Steps = append(run.Steps, ai.Step{Name: "build_context", Status: "succeeded", StartedAt: started, DurationMS: time.Since(started).Milliseconds(), Output: input})
	req, err := request(t.ModelName, input, args.Question, args.Context, t.Tokens)
	if err != nil {
		return result, err
	}
	if err = llm.CheckContextBudget(req, t.Tokens); err != nil {
		run.Steps = append(run.Steps, ai.Step{Name: "budget", Status: "failed", StartedAt: time.Now(), Error: err.Error()})
		return result, err
	}
	start := time.Now()
	out, err := t.Model.Call(ctx, req)
	step := ai.Step{Name: "model_call", Status: "succeeded", StartedAt: start, DurationMS: time.Since(start).Milliseconds(), Input: req, Output: map[string]any{"content": out.Content, "promptTokens": out.PromptTokens, "completionTokens": out.CompletionTokens}}
	if err != nil {
		step.Status = "failed"
		step.Error = err.Error()
	}
	run.Steps = append(run.Steps, step)
	if err != nil {
		return result, err
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
		err = ai.CheckSources(input.GroupID, answer.SourceMessageIDs, messages, args.Context != "general" && answer.Status != "insufficient_evidence")
	}
	step = ai.Step{Name: "validate_result", Status: "succeeded", StartedAt: time.Now()}
	if err != nil {
		step.Status = "failed"
		step.Error = err.Error()
	} else {
		run.Answer = &answer
		for _, id := range answer.SourceMessageIDs {
			run.Sources = append(run.Sources, messages[id])
		}
		run.Basis = "general"
		if len(answer.SourceMessageIDs) > 0 {
			run.Basis = "history"
			if args.Context == "mixed" {
				run.Basis = "mixed"
			}
		}
	}
	run.Steps = append(run.Steps, step)
	return result, err
}
func request(model string, input ai.Context, question, mode string, tokens int) (llm.Request, error) {
	data, err := json.Marshal(struct {
		Context  ai.Context `json:"context"`
		Question string     `json:"question"`
	}{input, question})
	if err != nil {
		return llm.Request{}, err
	}
	omitted := input.HistoryScope != nil && input.HistoryScope.EarlierMessagesOmitted
	return llm.Request{Model: model, Messages: []llm.Message{{Role: "system", Content: system(mode, omitted)}, {Role: "user", Content: string(data)}}, Format: Schema, Options: llm.Options{NumCtx: tokens, NumPredict: 2048}}, nil
}

func (t *Tool) LegacyArguments(s tools.Scope) (json.RawMessage, bool) {
	if s.LegacyMode == "qa" {
		return MarshalArguments(s.Question, "history"), true
	}
	if s.LegacyMode == "help" {
		return MarshalArguments(s.Question, "general"), true
	}
	return nil, false
}
