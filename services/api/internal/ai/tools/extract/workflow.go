package extracttool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/prompts"
	"github.com/LizHu95/Jetaime/services/api/internal/telemetry"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// extract runs the fixed todo extraction and evidence review workflow.
func (w *Tool) extract(ctx context.Context, input ai.Context, question string) (run ai.Run, err error) {
	ctx, span := telemetry.Start(ctx, "assistant.workflow", "CHAIN", attribute.String("workflow.kind", "extract"), attribute.String("session.id", input.GroupID), attribute.String("user.id", input.ActorID))
	defer func() {
		span.SetAttributes(attribute.String("workflow.run_id", run.ID), attribute.String("langfuse.observation.metadata.prompt_version", run.PromptVersion))
		for _, step := range run.Steps {
			span.AddEvent(step.Name, trace.WithTimestamp(step.StartedAt), trace.WithAttributes(attribute.String("step.status", step.Status), attribute.Int64("step.duration_ms", step.DurationMS)))
		}
		// Workflow errors may contain model-generated values. Export a stable category.
		var traceErr error
		if err != nil {
			traceErr = fmt.Errorf("workflow failed")
		}
		telemetry.End(span, traceErr)
	}()
	group, actor := input.GroupID, input.ActorID

	model := w.DefaultModel
	var identity *llm.Identity
	if provider, ok := w.ChatModel.(interface{ ModelIdentity() llm.Identity }); ok {
		v := provider.ModelIdentity()
		if v.Model != "" && v.Model != model {
			return run, fmt.Errorf("model is fixed at startup")
		}
		identity = &v
	}
	run = ai.Run{ID: "run-" + uuid.NewString(), GroupID: group, ActorID: actor, Kind: "extract", Question: question, Model: model, ModelIdentity: identity, PromptVersion: "extract-v20-scope-bounded", StartedAt: time.Now(), Status: "running", Steps: []ai.Step{}}
	defer func() {
		run.DurationMS = time.Since(run.StartedAt).Milliseconds()
		run.Status = "succeeded"
		if err != nil {
			run.Status = "failed"
			run.Error = err.Error()
		}
	}()
	run.Steps = append(run.Steps, ai.Step{Name: "build_context", Status: "succeeded", StartedAt: time.Now(), Output: input})
	payload := struct {
		Context  ai.Context `json:"context"`
		Question string     `json:"question,omitempty"`
	}{input, question}
	data, e := json.Marshal(payload)
	if e != nil {
		return run, e
	}
	request := llm.Request{Model: model, Messages: []llm.Message{{Role: "system", Content: prompts.Extract + "\nSchema: " + string(prompts.ExtractSchema)}, {Role: "user", Content: string(data)}}, Format: prompts.ExtractSchema, Options: llm.Options{NumCtx: w.Tokens, NumPredict: 2048}}
	if err = llm.CheckContextBudget(request, w.Tokens); err != nil {
		run.Steps = append(run.Steps, ai.Step{Name: "budget", Status: "failed", StartedAt: time.Now(), Error: err.Error()})
		return run, err
	}
	start := time.Now()
	output, e := w.ChatModel.Call(ctx, request)
	step := ai.Step{Name: "model_call", Status: "succeeded", StartedAt: start, DurationMS: time.Since(start).Milliseconds(), Input: request, Output: map[string]any{"content": output.Content, "promptTokens": output.PromptTokens, "completionTokens": output.CompletionTokens}}
	if e != nil {
		step.Status = "failed"
		step.Error = e.Error()
	}
	run.Steps = append(run.Steps, step)
	if e != nil {
		return run, e
	}
	start = time.Now()
	messages := map[string]ai.Message{}
	for _, m := range input.Messages {
		messages[m.ID] = m
	}
	members := map[string]bool{}
	for _, u := range input.Users {
		members[u.ID] = true
	}

	var extraction ai.Extraction
	extraction, err = decodeExtraction(output.Content)
	if err == nil {
		extraction.Suggestions = supplementReminders(input, extraction.Suggestions)
		extraction.Suggestions, err = w.supplementAcceptances(ctx, &run, input, extraction.Suggestions)
	}
	if err == nil && (extraction.Suggestions == nil || len(extraction.Suggestions) > 10) {
		err = fmt.Errorf("invalid suggestions array")
	}
	if err == nil {
		for i := range extraction.Suggestions {
			s := &extraction.Suggestions[i]
			if s.ID != "" || s.DueAt != nil || len(s.PossibleDuplicateIDs) > 0 || strings.TrimSpace(s.Title) == "" || len(s.Title) > 600 || (s.AssigneeID != nil && !members[*s.AssigneeID]) {
				err = fmt.Errorf("invalid task suggestion fields")
				break
			}
			if err = ai.CheckSources(group, s.SourceMessageIDs, messages, true); err != nil {
				break
			}
			s.ID = fmt.Sprintf("%s/%d", run.ID, i)
			s.DueAt = resolveTime(s.TimeExpression, s.SourceMessageIDs, messages)
			for _, t := range input.Tasks {
				if sameSources(t.SourceMessageIDs, s.SourceMessageIDs) || strings.TrimSpace(t.Title) == strings.TrimSpace(s.Title) {
					s.PossibleDuplicateIDs = append(s.PossibleDuplicateIDs, t.ID)
				}
			}
		}
	}
	if err == nil {
		var boundaryChanges []string
		extraction.Suggestions, boundaryChanges = enforceEvidenceBoundaries(input, extraction.Suggestions)
		run.Steps = append(run.Steps, ai.Step{Name: "candidate_evidence_boundaries", Status: "succeeded", StartedAt: time.Now(), Output: map[string]any{"changes": boundaryChanges, "keptCount": len(extraction.Suggestions)}})
		extraction.Suggestions, err = w.reviewActions(ctx, &run, input, extraction.Suggestions, messages)
	}
	if err == nil {
		var boundaryChanges []string
		extraction.Suggestions, boundaryChanges = enforceEvidenceBoundaries(input, extraction.Suggestions)
		run.Steps = append(run.Steps, ai.Step{Name: "evidence_boundaries", Status: "succeeded", StartedAt: time.Now(), Output: map[string]any{"changes": boundaryChanges, "keptCount": len(extraction.Suggestions)}})
		run.Suggestions = extraction.Suggestions
		seen := map[string]bool{}
		for _, s := range extraction.Suggestions {
			for _, id := range s.SourceMessageIDs {
				if !seen[id] {
					run.Sources = append(run.Sources, messages[id])
					seen[id] = true
				}
			}
		}
	}
	step = ai.Step{Name: "validate_result", Status: "succeeded", StartedAt: start, DurationNS: time.Since(start).Nanoseconds(), DurationMS: time.Since(start).Milliseconds()}
	if err != nil {
		step.Status = "failed"
		step.Error = err.Error()
		run.Suggestions = nil
		run.Sources = nil
	} else {
		step.Output = map[string]any{"suggestions": run.Suggestions, "sourceIDsVerified": true, "semanticAccuracy": "requires human review"}
	}
	run.Steps = append(run.Steps, step)
	return
}
func sameSources(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]bool{}
	for _, id := range a {
		m[id] = true
	}
	for _, id := range b {
		if !m[id] {
			return false
		}
	}
	return true
}
func resolveTime(expression string, ids []string, messages map[string]ai.Message) *time.Time {
	if expression != "今天" && expression != "明天" && expression != "后天" {
		return nil
	}
	var source ai.Message
	for _, id := range ids {
		m := messages[id]
		if strings.Contains(m.Text, expression) && m.SentAt.After(source.SentAt) {
			source = m
		}
	}
	if source.ID == "" {
		return nil
	}
	zone := source.SentAt.Location()
	y, m, d := source.SentAt.Date()
	date := time.Date(y, m, d, 0, 0, 0, 0, zone)
	if expression == "明天" {
		date = date.AddDate(0, 0, 1)
	}
	if expression == "后天" {
		date = date.AddDate(0, 0, 2)
	}
	return &date
}
