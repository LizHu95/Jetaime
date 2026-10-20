package extracttool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
)

type actionReview struct {
	CandidateIndex     int      `json:"candidateIndex"`
	Status             string   `json:"status"`
	EvidenceMessageIDs []string `json:"evidenceMessageIds"`
	Reason             string   `json:"reason"`
}

func filterReviewedActions(candidates []ai.Suggestion, reviews []actionReview, group string, messages map[string]ai.Message) ([]ai.Suggestion, error) {
	if len(reviews) != len(candidates) {
		return nil, fmt.Errorf("action review must cover every candidate")
	}
	seen := map[int]bool{}
	keep := map[int]bool{}
	for _, review := range reviews {
		i := review.CandidateIndex
		if i < 0 || i >= len(candidates) || seen[i] || strings.TrimSpace(review.Reason) == "" {
			return nil, fmt.Errorf("invalid action review coverage or reason")
		}
		seen[i] = true
		switch review.Status {
		case "active", "completed", "cancelled", "uncommitted", "uncertain":
		default:
			return nil, fmt.Errorf("invalid action review status")
		}
		if err := ai.CheckSources(group, review.EvidenceMessageIDs, messages, true); err != nil {
			return nil, err
		}
		keep[i] = review.Status == "active"
	}
	result := []ai.Suggestion{}
	for i, candidate := range candidates {
		if keep[i] {
			result = append(result, candidate)
		}
	}
	return result, nil
}

func (w *Tool) reviewUnresolvedActions(ctx context.Context, run *ai.Run, input ai.Context, candidates []ai.Suggestion, messages map[string]ai.Message) ([]ai.Suggestion, error) {
	if len(candidates) == 0 {
		return candidates, nil
	}
	payload, err := json.Marshal(struct {
		Context         ai.Context               `json:"context"`
		Candidates      []ai.Suggestion          `json:"candidates"`
		AcceptedActions []acceptedActionEvidence `json:"acceptedActions"`
	}{input, candidates, acceptanceReviewEvidence(input, candidates)})
	if err != nil {
		return nil, err
	}
	request := llm.Request{Model: run.Model, Messages: []llm.Message{{Role: "system", Content: actionReviewPrompt + "\nSchema: " + string(actionReviewSchema)}, {Role: "user", Content: string(payload)}}, Format: actionReviewSchema, Options: llm.Options{Temperature: 0, NumCtx: w.Tokens, NumPredict: 2048}}
	if err := llm.CheckContextBudget(request, w.Tokens); err != nil {
		return nil, err
	}
	start := time.Now()
	output, callErr := w.ChatModel.Call(ctx, request)
	step := ai.Step{Name: "review_model_call", Status: "succeeded", StartedAt: start, DurationNS: time.Since(start).Nanoseconds(), DurationMS: time.Since(start).Milliseconds(), Input: request, Output: map[string]any{"rawResponse": output.Raw, "content": output.Content, "promptTokens": output.PromptTokens, "completionTokens": output.CompletionTokens}}
	if callErr != nil {
		step.Status = "failed"
		step.Error = callErr.Error()
	}
	run.Steps = append(run.Steps, step)
	if callErr != nil {
		return nil, callErr
	}

	var response struct {
		Reviews []actionReview `json:"reviews"`
	}
	if err = ai.DecodeJSON([]byte(output.Content), &response); err != nil {
		return nil, err
	}
	var wire struct {
		Reviews []map[string]json.RawMessage `json:"reviews"`
	}
	if err = json.Unmarshal([]byte(output.Content), &wire); err != nil {
		return nil, err
	}
	for _, item := range wire.Reviews {
		if len(item) != 4 {
			return nil, fmt.Errorf("action review must contain exactly four fields")
		}
		for _, key := range []string{"candidateIndex", "status", "evidenceMessageIds", "reason"} {
			value, ok := item[key]
			if !ok || string(value) == "null" {
				return nil, fmt.Errorf("missing action review field %s", key)
			}
		}
	}
	filtered, err := filterReviewedActions(candidates, response.Reviews, input.GroupID, messages)
	if err != nil {
		return nil, err
	}
	run.Steps = append(run.Steps, ai.Step{Name: "review_actions", Status: "succeeded", StartedAt: time.Now(), Output: map[string]any{"reviews": response.Reviews, "candidateCount": len(candidates), "keptCount": len(filtered), "semanticAccuracy": "model judgment; requires human review"}})
	return filtered, nil
}

func (w *Tool) reviewActions(ctx context.Context, run *ai.Run, input ai.Context, candidates []ai.Suggestion, messages map[string]ai.Message) ([]ai.Suggestion, error) {
	// Once an action is accepted at the end of the supplied history there is no
	// later state transition to infer. Keep that verified acceptance directly,
	// unless an existing task still requires duplicate review.
	active := map[string]bool{}
	unresolved := []ai.Suggestion{}
	for index, candidate := range candidates {
		terminal := false
		for _, evidence := range acceptanceReviewEvidence(input, candidates) {
			if evidence.CandidateIndex == index && len(input.Messages) > 0 && evidence.AcceptanceMessageID == input.Messages[len(input.Messages)-1].ID && len(candidate.PossibleDuplicateIDs) == 0 {
				terminal = true
			}
		}
		if terminal {
			active[candidate.ID] = true
		} else {
			unresolved = append(unresolved, candidate)
		}
	}
	if len(active) > 0 {
		run.Steps = append(run.Steps, ai.Step{Name: "terminal_acceptance_state", Status: "succeeded", StartedAt: time.Now(), Output: map[string]any{"acceptedAsActiveCount": len(active), "reason": "verified request/acceptance pair ends supplied history; no later transition or existing duplicate"}})
	}
	reviewed, err := w.reviewUnresolvedActions(ctx, run, input, unresolved, messages)
	if err != nil {
		return nil, err
	}
	for _, candidate := range reviewed {
		active[candidate.ID] = true
	}
	result := []ai.Suggestion{}
	for _, candidate := range candidates {
		if active[candidate.ID] {
			result = append(result, candidate)
		}
	}
	return result, nil
}
