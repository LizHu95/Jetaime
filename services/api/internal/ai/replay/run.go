package replay

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	mainagent "github.com/LizHu95/Jetaime/services/api/internal/ai/agent/main"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/amap"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/catalog"
	historytool "github.com/LizHu95/Jetaime/services/api/internal/ai/tools/history"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/qa"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/recent"
	remindertool "github.com/LizHu95/Jetaime/services/api/internal/ai/tools/reminder"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/respond"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/silence"
	"github.com/LizHu95/Jetaime/services/api/internal/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

type recordingModel struct {
	base  llm.ChatModel
	calls []ModelCall
}

func (m *recordingModel) ModelIdentity() llm.Identity {
	if provider, ok := m.base.(interface{ ModelIdentity() llm.Identity }); ok {
		return provider.ModelIdentity()
	}
	return llm.Identity{}
}
func (m *recordingModel) Call(ctx context.Context, req llm.Request) (llm.Result, error) {
	start := time.Now()
	result, err := m.base.Call(ctx, req)
	identity := m.ModelIdentity()
	name := identity.Model
	if name == "" {
		name = req.Model
	}
	m.calls = append(m.calls, ModelCall{Model: name, InputByteEstimate: llm.InputTokenUpperBound(req), PromptTokens: result.PromptTokens, CompletionTokens: result.CompletionTokens, DurationMS: time.Since(start).Milliseconds(), Error: err != nil})
	return result, err
}

// Run creates every dependency afresh. Only the caller-supplied model can perform
// inference I/O. Maps, history and reminders have no network or database handles.
func Run(ctx context.Context, snapshot *Snapshot, c Case, model llm.ChatModel, modelName string, tokens, repetition int) (Result, error) {
	v, err := snapshot.View(c)
	if err != nil {
		return Result{}, err
	}
	if model == nil {
		return Result{}, fmt.Errorf("model is required")
	}
	ctx, span := telemetry.Start(ctx, "eval.replay", "CHAIN", attribute.String("eval.case_id", c.ID), attribute.String("eval.snapshot_sha256", snapshot.SHA256), attribute.Int("eval.repetition", repetition), attribute.String("session.id", "eval:"+c.ID), attribute.String("user.id", c.ActorID), attribute.String("chat.trigger_mode", "reactive"), attribute.String("chat.trigger_source", "eval_replay"))
	result := Result{CaseID: c.ID, Repetition: repetition, Status: "failed", Scores: map[string]bool{}}
	if span.SpanContext().IsValid() {
		result.TraceID = span.SpanContext().TraceID().String()
	}
	defer func() { telemetry.End(span, err) }()
	recorded := &recordingModel{base: model}
	maps := &fixtureCaller{fixtures: c.MapFixtures}
	reminders := &memoryReminders{group: c.GroupID, actor: c.ActorID, now: c.At}
	guard := func(_ context.Context, s tools.Scope) error {
		return v.authorize(s.Context.GroupID, s.Context.ActorID, s.ContextEndSeq)
	}
	entries := []tools.Tool{respond.New(), silence.New(), qa.New(recorded, modelName, tokens, v.QAContext), recent.New(v), historytool.New(recorded, modelName, tokens, v), remindertool.New(reminders, reminderOperation)}
	entries = append(entries, amap.New(maps, guard)...)
	registry, err := catalog.New(entries...)
	if err != nil {
		return result, err
	}
	agent, err := mainagent.NewAgent(recorded, modelName, tokens, registry)
	if err != nil {
		return result, err
	}
	start := time.Now()
	run, err := agent.ExecuteMain(ctx, v.Input, c.Question, nil, v.Fence)
	result.Run = run
	result.Answer = run.Answer
	result.Status = run.Status
	if err != nil {
		result.Error = err.Error()
	}
	result.SimulatedReminderOperations = reminders.operations
	result.MapFixtureMisses = maps.misses
	result.Metrics = Metrics{ModelCalls: recorded.calls, ToolCalls: []string{}, DurationMS: time.Since(start).Milliseconds()}
	prompt, completion := 0, 0
	allPrompt, allCompletion := len(recorded.calls) > 0, len(recorded.calls) > 0
	for i, call := range recorded.calls {
		result.Metrics.CumulativeInputByteEstimate += call.InputByteEstimate
		if i == 0 {
			result.Metrics.FirstInputByteEstimate = call.InputByteEstimate
		}
		if call.PromptTokens == nil {
			allPrompt = false
		} else {
			prompt += *call.PromptTokens
		}
		if call.CompletionTokens == nil {
			allCompletion = false
		} else {
			completion += *call.CompletionTokens
		}
	}
	if allPrompt {
		result.Metrics.PromptTokens = &prompt
	}
	if allCompletion {
		result.Metrics.CompletionTokens = &completion
	}
	for _, step := range run.Steps {
		if step.Name == "tool_call" {
			raw, _ := json.Marshal(step.Output)
			var call tools.Call
			if json.Unmarshal(raw, &call) == nil {
				result.Metrics.ToolCalls = append(result.Metrics.ToolCalls, call.Name)
				if call.Name == "tool_search" {
					result.Metrics.SearchCalls++
				}
			}
		}
	}
	result.Scores = Score(c, result)
	return result, nil
}
func Score(c Case, r Result) map[string]bool {
	scores := map[string]bool{"execution_success": r.Status == "succeeded", "map_fixtures_complete": r.MapFixtureMisses == 0}
	if !c.Reviewed {
		return scores
	}
	called := map[string]bool{}
	for _, name := range r.Metrics.ToolCalls {
		called[name] = true
	}
	if len(c.Expected.RequiredTools) > 0 {
		ok := true
		for _, name := range c.Expected.RequiredTools {
			ok = ok && called[name]
		}
		scores["required_tools"] = ok
	}
	if len(c.Expected.ForbiddenTools) > 0 {
		ok := true
		for _, name := range c.Expected.ForbiddenTools {
			ok = ok && !called[name]
		}
		scores["forbidden_tools"] = ok
	}
	if len(c.Expected.AnswerContainsAll) > 0 {
		ok := r.Answer != nil
		for _, word := range c.Expected.AnswerContainsAll {
			ok = ok && r.Answer != nil && strings.Contains(r.Answer.Answer, word)
		}
		scores["answer_terms"] = ok
	}
	if len(c.Expected.RequiredSourceIDs) > 0 {
		found := map[string]bool{}
		if r.Answer != nil {
			for _, id := range r.Answer.SourceMessageIDs {
				found[id] = true
			}
		}
		ok := true
		for _, id := range c.Expected.RequiredSourceIDs {
			ok = ok && found[id]
		}
		scores["required_sources"] = ok
	}
	return scores
}
