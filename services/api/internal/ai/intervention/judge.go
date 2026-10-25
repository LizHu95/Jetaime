// Package intervention judges whether a passive conversation needs AI help.
package intervention

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/skills"
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
	if err := skills.Validate(); err != nil {
		return nil, err
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
		evidenceErr := validateRecognition(result.Decision, combined)
		var rejection *candidateRejection
		if evidenceErr != nil && !errors.As(evidenceErr, &rejection) {
			return evidenceErr
		}
		if !result.Decision.ShouldIntervene {
			return evidenceErr
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
		return evidenceErr
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
			var rejection *candidateRejection
			if callErr == nil && !formatError && errors.As(err, &rejection) && rejectIntervention(output, rejection) {
				// An evidence gate declining to intervene is a successful silent
				// outcome, not a broken workflow. Keep the model's rejected proposal
				// for quality review without setting an error on the step or run.
				step.Status = "succeeded"
				step.Output = map[string]any{"outcome": "candidate_rejected", "reasonCode": rejection.Code, "reason": rejection.Reason, "breakdownType": rejection.Breakdown, "modelOutput": out.Content, "decision": output}
				steps = append(steps, step)
				tools.TraceStep(ctx, step)
				return steps, nil
			}
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
	if d.TopicContext == nil {
		if d.Action != "skip" {
			return invalid()
		}
	} else {
		topic := d.TopicContext
		if !validText(topic.Topic, 500) || (topic.Continuity != "new_topic" && topic.Continuity != "continuation" && topic.Continuity != "uncertain") {
			return invalid()
		}
		available, selected, anchors := map[string]bool{}, map[string]bool{}, map[string]bool{}
		for _, m := range input.Messages {
			available[m.ID] = true
		}
		for _, id := range topic.MessageIDs {
			if !available[id] || selected[id] {
				return invalid()
			}
			selected[id] = true
		}
		for _, id := range topic.AnchorMessageIDs {
			if !selected[id] || anchors[id] {
				return invalid()
			}
			anchors[id] = true
		}
		if !anchors[input.CurrentMessageID] {
			return invalid()
		}
		evidence := []string{}
		if d.Intent != nil {
			evidence = append(evidence, d.Intent.EvidenceMessageIDs...)
		}
		if d.Verification != nil {
			evidence = append(evidence, d.Verification.EvidenceMessageIDs...)
		}
		for _, breakdown := range d.Breakdowns {
			evidence = append(evidence, breakdown.EvidenceMessageIDs...)
		}
		for _, id := range evidence {
			if !selected[id] {
				return invalid()
			}
		}
	}
	decision := ai.Recognition{ShouldIntervene: d.Action == "intervene", Reason: d.Reason, Breakdowns: d.Breakdowns, Intent: d.Intent}
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
	return validateRecognition(decision, input)
}

// candidateRejection is an ordinary evidence-gate outcome. Invalid sources,
// malformed decisions and authorization violations remain actual errors.
type candidateRejection struct {
	Code, Reason, Breakdown string
}

func (r *candidateRejection) Error() string { return r.Code + ": " + r.Reason }

func rejectIntervention(output any, rejection *candidateRejection) bool {
	switch decision := output.(type) {
	case *ai.InterventionCandidate:
		*decision = ai.InterventionCandidate{Action: "skip", Reason: rejection.Reason, Breakdowns: []ai.Breakdown{}}
	case *ai.Recognition:
		*decision = ai.Recognition{Reason: rejection.Reason, Breakdowns: []ai.Breakdown{}}
	default:
		return false
	}
	return true
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
	messages := map[string]ai.Message{}
	for _, m := range input.Messages {
		ids[m.ID] = true
		messages[m.ID] = m
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
	var rejection *candidateRejection
	for _, b := range d.Breakdowns {
		if (b.Type == "forgotten_constraint" || b.Type == "goal_drift" || b.Type == "risky_commitment") && len(b.EvidenceMessageIDs) < 2 {
			return invalid()
		}
		if !check(b.EvidenceMessageIDs) {
			return invalid()
		}
		if _, communication := skills.Route(b.Type); communication {
			// A relational breakdown needs actual interaction evidence. AI replies,
			// one person's repeated messages and missing identities cannot supply it.
			speakers := map[string]bool{}
			for _, id := range b.EvidenceMessageIDs {
				message := messages[id]
				if message.SenderID == "" || (message.SenderType != "" && message.SenderType != "user") {
					return invalid()
				}
				speakers[message.SenderID] = true
			}
			if len(speakers) < 2 {
				rejection = &candidateRejection{Code: "insufficient_participant_evidence", Reason: "沟通障碍证据仅来自一位成员，暂不介入，等待另一位成员回应", Breakdown: b.Type}
			}
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
	// Validate every source and intent before returning a non-error outcome:
	// a rejected proposal must not mask an unrelated invalid reference or write.
	if rejection != nil {
		return rejection
	}
	return nil
}
