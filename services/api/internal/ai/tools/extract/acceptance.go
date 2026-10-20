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
)

const acceptancePrompt = prompts.Base + `
任务：只检查输入的相邻真人消息对是否形成行动接受。“称呼＋来/帮我＋动作”是向当前对话接收者发出的请求，开头的亲昵称呼不应被当作另一个第三人。第一条必须提出具体行动请求，第二条肯定回应且语义明确指向该请求，才生成suggestion；普通聊天的附和、庆祝不算接受。
若A请求B做某事，B回复“好”，B接受了该行动，负责人是B，来源必须同时包含两条消息。B无需再次重述动作；不能把这种明确接受漏掉。title只写动作，去掉开头称呼；请求里的“我”是请求者，改成“对方”，不能保留第一人称造成负责人混淆。例如A说“小宝来帮我拿包”，B回“好”，title为“帮对方拿包”，负责人B，而非小宝或A。不能写“回复好”。时间只用请求原文，没有则空字符串。检查tasks避免重复；后续状态另由独立核对处理。不符合条件返回空suggestions。只输出Schema JSON。`

type acceptancePair struct {
	Request  ai.Message `json:"request"`
	Response ai.Message `json:"response"`
}

func uncoveredAcceptancePairs(input ai.Context, candidates []ai.Suggestion) []acceptancePair {
	aiMembers := aiMemberIDs(input.Users)
	pairs := []acceptancePair{}
	for i := 1; i < len(input.Messages); i++ {
		previous, current := input.Messages[i-1], input.Messages[i]
		if previous.SenderID == current.SenderID || aiMembers[previous.SenderID] || aiMembers[current.SenderID] {
			continue
		}
		reply := strings.Trim(strings.TrimSpace(current.Text), "。.!！~～，, ")
		switch reply {
		case "好", "好的", "好呀", "好哒", "可以", "行", "没问题":
		default:
			continue
		}
		covered := false
		for _, candidate := range candidates {
			for _, id := range candidate.SourceMessageIDs {
				if id == current.ID {
					covered = true
				}
			}
		}
		if !covered {
			pairs = append(pairs, acceptancePair{previous, current})
		}
	}
	return pairs
}

func (w *Tool) supplementAcceptances(ctx context.Context, run *ai.Run, input ai.Context, candidates []ai.Suggestion) ([]ai.Suggestion, error) {
	pairs := uncoveredAcceptancePairs(input, candidates)
	if len(pairs) == 0 {
		return candidates, nil
	}
	payload, err := json.Marshal(struct {
		Users []ai.User        `json:"users"`
		Pairs []acceptancePair `json:"pairs"`
		Tasks []ai.Task        `json:"tasks"`
	}{input.Users, pairs, input.Tasks})
	if err != nil {
		return nil, err
	}
	request := llm.Request{Model: run.Model, Messages: []llm.Message{{Role: "system", Content: acceptancePrompt + "\nSchema: " + string(prompts.ExtractSchema)}, {Role: "user", Content: string(payload)}}, Format: prompts.ExtractSchema, Options: llm.Options{Temperature: 0, NumCtx: w.Tokens, NumPredict: 2048}}
	if err := llm.CheckContextBudget(request, w.Tokens); err != nil {
		return nil, err
	}
	start := time.Now()
	result, err := w.ChatModel.Call(ctx, request)
	step := ai.Step{Name: "acceptance_model_call", Status: "succeeded", StartedAt: start, DurationNS: time.Since(start).Nanoseconds(), DurationMS: time.Since(start).Milliseconds(), Input: request, Output: map[string]any{"rawResponse": result.Raw, "content": result.Content, "promptTokens": result.PromptTokens, "completionTokens": result.CompletionTokens}}
	if err != nil {
		step.Status = "failed"
		step.Error = err.Error()
	}
	run.Steps = append(run.Steps, step)
	if err != nil {
		return nil, err
	}
	var extra ai.Extraction
	extra, err = decodeExtraction(result.Content)
	if err != nil {
		return nil, err
	}
	if extra.Suggestions == nil || len(extra.Suggestions) > 10 {
		return nil, fmt.Errorf("invalid acceptance suggestions")
	}
	// Supplement may only cite a complete inspected pair. Validate its remaining
	// fields with the same workflow checks as the main model output.
	accepted := []ai.Suggestion{}
	rejected := 0
	for _, candidate := range extra.Suggestions {
		matched := false
		for _, pair := range pairs {
			if sameSources(candidate.SourceMessageIDs, []string{pair.Request.ID, pair.Response.ID}) && candidate.AssigneeID != nil && *candidate.AssigneeID == pair.Response.SenderID {
				matched = true
				if !strings.Contains(compactEvidenceText(pair.Request.Text), compactEvidenceText(candidate.TimeExpression)) {
					candidate.TimeExpression = ""
				}
			}
		}
		if !matched {
			rejected++
			continue
		}
		duplicate := false
		for _, existing := range append(candidates, accepted...) {
			if existing.Title == candidate.Title && sameSources(existing.SourceMessageIDs, candidate.SourceMessageIDs) && existing.AssigneeID != nil && *existing.AssigneeID == *candidate.AssigneeID {
				duplicate = true
			}
		}
		if !duplicate {
			accepted = append(accepted, candidate)
		}
	}
	run.Steps = append(run.Steps, ai.Step{Name: "acceptance_evidence", Status: "succeeded", StartedAt: time.Now(), Output: map[string]any{"acceptedCount": len(accepted), "rejectedCount": rejected}})
	return append(candidates, accepted...), nil
}

func decodeExtraction(content string) (ai.Extraction, error) {
	var extraction ai.Extraction
	if err := ai.DecodeJSON([]byte(content), &extraction); err != nil {
		return extraction, err
	}
	if extraction.Suggestions == nil || len(extraction.Suggestions) > 10 {
		return extraction, fmt.Errorf("invalid suggestions array")
	}
	var wire struct {
		Suggestions []map[string]json.RawMessage `json:"suggestions"`
	}
	if err := json.Unmarshal([]byte(content), &wire); err != nil {
		return extraction, err
	}
	for _, item := range wire.Suggestions {
		if len(item) != 4 || string(item["timeExpression"]) == "null" {
			return extraction, fmt.Errorf("suggestion must contain exactly the four schema fields")
		}
		for _, key := range []string{"title", "assigneeId", "timeExpression", "sourceMessageIds"} {
			if _, ok := item[key]; !ok {
				return extraction, fmt.Errorf("missing suggestion field %s", key)
			}
		}
	}
	return extraction, nil
}

// Carry the already-classified and structurally verified acceptance relation
// into state review, which should check later transitions rather than invent a
// different actor for the same pair.
type acceptedActionEvidence struct {
	CandidateIndex      int    `json:"candidateIndex"`
	RequesterID         string `json:"requesterId"`
	ResponderID         string `json:"responderId"`
	RequestMessageID    string `json:"requestMessageId"`
	AcceptanceMessageID string `json:"acceptanceMessageId"`
}

func acceptanceReviewEvidence(input ai.Context, candidates []ai.Suggestion) []acceptedActionEvidence {
	evidence := []acceptedActionEvidence{}
	for index, candidate := range candidates {
		for _, pair := range uncoveredAcceptancePairs(input, nil) {
			if sameSources(candidate.SourceMessageIDs, []string{pair.Request.ID, pair.Response.ID}) && candidate.AssigneeID != nil && *candidate.AssigneeID == pair.Response.SenderID {
				evidence = append(evidence, acceptedActionEvidence{index, pair.Request.SenderID, pair.Response.SenderID, pair.Request.ID, pair.Response.ID})
			}
		}
	}
	return evidence
}
