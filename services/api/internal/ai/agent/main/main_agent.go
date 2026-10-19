package mainagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	aicontext "github.com/LizHu95/Jetaime/services/api/internal/ai/context"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/prompts"
	"github.com/LizHu95/Jetaime/services/api/internal/llm"
	"github.com/LizHu95/Jetaime/services/api/internal/telemetry"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
)

// ExecuteMain makes one model call for ordinary answers. Only an explicit
// extraction decision loads a separate authorized snapshot for the internal
// extraction capability. All calls pass the final context budget check.
func (a *Agent) ExecuteMain(ctx context.Context, input ai.Context, question string, load ai.ExtractContextLoader) (run ai.Run, err error) {
	ctx, span := telemetry.Start(ctx, "assistant.main", "CHAIN")
	started := time.Now()
	run = ai.Run{ID: "run-" + uuid.NewString(), GroupID: input.GroupID, ActorID: input.ActorID, Kind: "main", Question: question, Model: a.DefaultModel, PromptVersion: "main-v2-single-agent", StartedAt: started, Status: "running", Steps: []ai.Step{}}
	defer func() {
		run.DurationMS = time.Since(started).Milliseconds()
		run.Status = "succeeded"
		var traceErr error
		if err != nil {
			run.Status = "failed"
			run.Error = err.Error()
			traceErr = errors.New("main agent failed")
		}
		span.SetAttributes(attribute.String("agent.result_kind", run.Kind), attribute.String("llm.prompt_template.version", "main-v2-single-agent"))
		telemetry.End(span, traceErr)
	}()
	if err = ctx.Err(); err != nil {
		return
	}
	if err = ai.ValidateContext(input); err != nil {
		return
	}
	if strings.TrimSpace(question) == "" || len(question) > 3000 {
		err = fmt.Errorf("question must be 1–3000 bytes")
		return
	}
	if input.Request == nil {
		input, err = aicontext.RecentRouteContext(input, question, a.Tokens, input.HistoryScope != nil && input.HistoryScope.EarlierMessagesOmitted)
		if err != nil {
			return
		}
	}
	run.HistoryScope = input.HistoryScope
	if provider, ok := a.ChatModel.(interface{ ModelIdentity() llm.Identity }); ok {
		identity := provider.ModelIdentity()
		if identity.Model != "" && identity.Model != a.DefaultModel {
			err = fmt.Errorf("model is fixed at startup; restart to switch")
			return
		}
		run.ModelIdentity = &identity
	}
	run.Steps = append(run.Steps, ai.Step{Name: "build_context", Status: "succeeded", StartedAt: time.Now(), Output: input})
	request, e := prompts.MainRequest(a.DefaultModel, input, question, a.Tokens)
	if e != nil {
		err = e
		return
	}
	if err = llm.CheckContextBudget(request, a.Tokens); err != nil {
		run.Steps = append(run.Steps, ai.Step{Name: "budget", Status: "failed", StartedAt: time.Now(), Error: err.Error()})
		return
	}
	callStarted := time.Now()
	output, e := a.ChatModel.Call(ctx, request)
	step := ai.Step{Name: "model_call", Status: "succeeded", StartedAt: callStarted, DurationMS: time.Since(callStarted).Milliseconds(), Input: request, Output: map[string]any{"content": output.Content, "promptTokens": output.PromptTokens, "completionTokens": output.CompletionTokens}}
	if e != nil {
		step.Status = "failed"
		step.Error = e.Error()
	}
	run.Steps = append(run.Steps, step)
	if e != nil {
		err = e
		return
	}
	decision, e := decodeMainDecision(output.Content, input)
	if e != nil {
		err = e
		run.Steps = append(run.Steps, ai.Step{Name: "validate_result", Status: "failed", StartedAt: time.Now(), Error: err.Error()})
		return
	}
	run.Steps = append(run.Steps, ai.Step{Name: "validate_result", Status: "succeeded", StartedAt: time.Now()})
	if decision.Action == "answer" {
		run.Answer = decision.Answer
		run.Basis = decision.Basis
		run.Route = &ai.RouteDecision{Kind: "main", Version: "main-v2-single-agent"}
		messages := map[string]ai.Message{}
		for _, message := range input.Messages {
			messages[message.ID] = message
		}
		for _, id := range run.Answer.SourceMessageIDs {
			run.Sources = append(run.Sources, messages[id])
		}
		return
	}
	if load == nil {
		err = fmt.Errorf("extraction capability unavailable")
		return
	}
	extractionInput, e := load(ctx)
	if e != nil {
		err = e
		return
	}
	if extractionInput.GroupID != input.GroupID || extractionInput.ActorID != input.ActorID {
		err = fmt.Errorf("extraction context authorization changed")
		return
	}
	inheritRequestContext(&extractionInput, input)
	extractionCtx, extractionSpan := telemetry.Start(ctx, "assistant.capability.extract", "CHAIN")
	child, e := a.Execute(extractionCtx, extractionInput, "extract", question)
	var extractionErr error
	if e != nil {
		extractionErr = errors.New("extraction capability failed")
	}
	telemetry.End(extractionSpan, extractionErr)
	child.Route = &ai.RouteDecision{Kind: "extract", Version: "main-v2-single-agent"}
	child.StartedAt = started
	child.Steps = append(run.Steps, child.Steps...)
	run = child
	err = e
	return
}

func decodeMainDecision(content string, input ai.Context) (mainDecision, error) {
	var decision mainDecision
	if ai.DecodeJSON([]byte(content), &decision) != nil {
		return decision, ErrMainDecision
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(content), &fields) != nil || fields["answer"] == nil {
		return decision, ErrMainDecision
	}
	if decision.Action == "extract" {
		if decision.Answer != nil || decision.Basis != "none" {
			return decision, ErrMainDecision
		}
		return decision, nil
	}
	if decision.Action != "answer" || decision.Answer == nil || (decision.Basis != "general" && decision.Basis != "history" && decision.Basis != "mixed") {
		return decision, ErrMainDecision
	}
	answer := decision.Answer
	if strings.TrimSpace(answer.Answer) == "" || len(answer.Answer) > 32000 || strings.ContainsRune(answer.Answer, '\x00') || answer.SourceMessageIDs == nil || (answer.Status != "answered" && answer.Status != "insufficient_evidence" && answer.Status != "conflicting_evidence") {
		return decision, ErrMainDecision
	}
	messages := map[string]ai.Message{}
	for _, message := range input.Messages {
		messages[message.ID] = message
	}
	required := decision.Basis != "general" && answer.Status != "insufficient_evidence"
	if ai.CheckSources(input.GroupID, answer.SourceMessageIDs, messages, required) != nil {
		return decision, ErrMainDecision
	}
	return decision, nil
}

func inheritRequestContext(child *ai.Context, parent ai.Context) {
	if parent.Request == nil {
		return
	}
	child.Request = parent.Request
	present := map[string]bool{}
	for _, message := range child.Messages {
		present[message.ID] = true
	}
	required := map[string]bool{}
	for _, id := range parent.Request.RelatedMessageIDs {
		required[id] = true
	}
	var missing []ai.Message
	for _, message := range parent.Messages {
		if required[message.ID] && !present[message.ID] {
			missing = append(missing, message)
			present[message.ID] = true
		}
	}
	child.Messages = append(missing, child.Messages...)
}

type mainDecision struct {
	Action string     `json:"action"`
	Basis  string     `json:"basis"`
	Answer *ai.Answer `json:"answer"`
}
