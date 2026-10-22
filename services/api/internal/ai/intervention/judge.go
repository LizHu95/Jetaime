// Package intervention judges whether a passive conversation needs AI help.
package intervention

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
	"github.com/google/jsonschema-go/jsonschema"
)

// Judge only recognizes intent; it has no agent or tool registry.
type Judge struct {
	model  llm.ChatModel
	name   string
	tokens int
}

func New(model llm.ChatModel, name string, tokens int) (*Judge, error) {
	if model == nil || strings.TrimSpace(name) == "" || tokens < 4096 {
		return nil, fmt.Errorf("intervention judge requires model and context >=4096")
	}
	return &Judge{model: model, name: name, tokens: tokens}, nil
}

func (j *Judge) Discover(ctx context.Context, input ai.RecognitionInput) (result ai.CandidateResult, err error) {
	result.Model, result.PromptVersion = j.name, recognitionVersion
	if input.TriggerMode != "proactive" || len(input.Messages) == 0 || len(input.Messages) > ai.InterventionMessageLimit || input.Messages[len(input.Messages)-1].ID != input.CurrentMessageID {
		return result, ai.ErrInvalidDecision
	}
	resolved, schema, err := candidateSchema()
	if err != nil {
		return result, err
	}
	result.Steps, err = j.call(ctx, "discover_intervention", recognitionPrompt, input, resolved, schema, &result.Decision, func() error { return validateCandidate(result.Decision, input) })
	return result, err
}

func (j *Judge) Decide(ctx context.Context, input ai.VerificationInput) (result ai.RecognitionResult, err error) {
	result.Model, result.PromptVersion = j.name, recognitionVersion
	if input.Candidate.Action != "verify" || validateCandidate(input.Candidate, input.Conversation) != nil || len(input.Evidence.Messages) == 0 || len(input.Evidence.Messages) > ai.MaxInterventionEvidenceMessages || input.Evidence.Coverage != "partial" {
		return result, ai.ErrInvalidDecision
	}
	resolved, schema, err := recognitionSchema()
	if err != nil {
		return result, err
	}
	combined := input.Conversation
	combined.Messages = append(append([]ai.Message{}, combined.Messages...), input.Evidence.Messages...)
	result.Steps, err = j.call(ctx, "decide_intervention", verificationPrompt, input, resolved, schema, &result.Decision, func() error {
		if err := validateRecognition(result.Decision, combined); err != nil {
			return err
		}
		if !result.Decision.ShouldIntervene {
			return nil
		}
		// Verification is read-only and cannot manufacture a new operation request.
		if result.Decision.Intent.Type == "execute_request" {
			return ai.ErrInvalidDecision
		}
		recent := map[string]bool{}
		for _, message := range input.Conversation.Messages {
			recent[message.ID] = true
		}
		historical, candidate := false, false
		for _, id := range result.Decision.Intent.EvidenceMessageIDs {
			historical = historical || !recent[id]
			for _, source := range input.Candidate.Verification.EvidenceMessageIDs {
				candidate = candidate || id == source
			}
		}
		if !historical || !candidate {
			return ai.ErrInvalidDecision
		}
		return nil
	})
	return result, err
}

// Each model stage gets one structural repair. Semantic/authorization failures
// stop immediately; neither stage can request another retrieval or execute tools.
func (j *Judge) call(ctx context.Context, name, prompt string, input any, resolved *jsonschema.Resolved, schema json.RawMessage, output any, validate func() error) ([]ai.Step, error) {
	payload, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	req := llm.Request{Model: j.name, Messages: []llm.Message{
		{Role: "system", Content: prompt + "\n输出JSON Schema（必须严格遵守）：" + string(schema)},
		{Role: "user", Content: string(payload)},
	}, Format: schema, Options: llm.Options{NumCtx: j.tokens, NumPredict: 2048}}
	steps := []ai.Step{}
	for attempt := 0; attempt < 2; attempt++ {
		if err = ctx.Err(); err != nil {
			return steps, err
		}
		if err = llm.CheckContextBudget(req, j.tokens); err != nil {
			return steps, err
		}
		started := time.Now()
		out, callErr := j.model.Call(ctx, req)
		step := ai.Step{Name: name, StartedAt: started, DurationMS: time.Since(started).Milliseconds(), Input: input, Status: "failed"}
		err = callErr
		formatError := false
		if err == nil {
			err = decodeDecision(out.Content, resolved, output)
			formatError = err != nil
			if err == nil {
				err = validate()
			}
		}
		if err == nil {
			step.Status, step.Output = "succeeded", output
		} else {
			step.Error = err.Error()
			if callErr == nil {
				step.Output = map[string]any{"modelOutput": out.Content, "validationError": err.Error()}
			}
		}
		steps = append(steps, step)
		tools.TraceStep(ctx, step)
		if err == nil {
			return steps, nil
		}
		if !formatError || attempt == 1 {
			return steps, err
		}
		feedback, _ := json.Marshal(map[string]string{"validationError": err.Error()})
		req.Messages = append(req.Messages,
			llm.Message{Role: "assistant", Content: out.Content},
			llm.Message{Role: "user", Content: recognitionRepairPrompt(string(feedback))},
		)
	}
	return steps, ai.ErrInvalidDecision
}

func decodeDecision(content string, resolved *jsonschema.Resolved, output any) error {
	var value any
	if err := json.Unmarshal([]byte(content), &value); err != nil {
		return fmt.Errorf("%w: recognition JSON: %v", ai.ErrInvalidDecision, err)
	}
	if err := resolved.Validate(value); err != nil {
		return fmt.Errorf("%w: recognition schema: %v", ai.ErrInvalidDecision, err)
	}
	if err := ai.DecodeJSON([]byte(content), output); err != nil {
		return fmt.Errorf("%w: recognition fields: %v", ai.ErrInvalidDecision, err)
	}
	return nil
}

func validateCandidate(d ai.InterventionCandidate, input ai.RecognitionInput) error {
	invalid := func() error { return fmt.Errorf("%w: invalid intervention candidate", ai.ErrInvalidDecision) }
	decision := ai.Recognition{ShouldIntervene: d.Action == "intervene", Reason: d.Reason, Breakdowns: d.Breakdowns, Intent: d.Intent}
	if err := validateRecognition(decision, input); err != nil {
		return err
	}
	switch d.Action {
	case "skip", "intervene":
		if d.Verification != nil {
			return invalid()
		}
	case "verify":
		if d.Intent != nil || d.Verification == nil || len(d.Breakdowns) != 0 {
			return invalid()
		}
		question := d.Verification
		if !validText(question.Topic, 500) || !validText(question.Query, 1500) || len(question.EvidenceMessageIDs) == 0 {
			return invalid()
		}
		ids, seen := map[string]bool{}, map[string]bool{}
		for _, m := range input.Messages {
			ids[m.ID] = true
		}
		for _, id := range question.EvidenceMessageIDs {
			if !ids[id] || seen[id] {
				return invalid()
			}
			seen[id] = true
		}
	default:
		return invalid()
	}
	return nil
}

func validText(value string, max int) bool {
	return strings.TrimSpace(value) != "" && len(value) <= max && !strings.ContainsRune(value, '\x00')
}

func validateRecognition(d ai.Recognition, input ai.RecognitionInput) error {
	invalid := func() error { return fmt.Errorf("%w: invalid intervention evidence or intent", ai.ErrInvalidDecision) }
	if strings.TrimSpace(d.Reason) == "" || strings.ContainsRune(d.Reason, '\x00') || len(d.Reason) > 2000 || d.ShouldIntervene != (d.Intent != nil) {
		return invalid()
	}
	ids := map[string]bool{}
	for _, m := range input.Messages {
		ids[m.ID] = true
	}
	check := func(evidence []string) bool {
		if len(evidence) == 0 {
			return false
		}
		seen := map[string]bool{}
		for _, id := range evidence {
			if !ids[id] || seen[id] {
				return false
			}
			seen[id] = true
		}
		return true
	}
	for _, b := range d.Breakdowns {
		if (b.Type == "forgotten_constraint" || b.Type == "goal_drift" || b.Type == "risky_commitment") && len(b.EvidenceMessageIDs) < 2 {
			return invalid()
		}
		if !check(b.EvidenceMessageIDs) {
			return invalid()
		}
	}
	if d.Intent != nil {
		if strings.TrimSpace(d.Intent.Objective) == "" || strings.ContainsRune(d.Intent.Objective, '\x00') || len(d.Intent.Objective) > 2000 || !check(d.Intent.EvidenceMessageIDs) {
			return invalid()
		}
		if d.Intent.Type == "execute_request" {
			current := false
			for _, id := range d.Intent.EvidenceMessageIDs {
				if id == input.CurrentMessageID {
					current = true
				}
			}
			if !current {
				return invalid()
			}
		}
	}
	return nil
}
