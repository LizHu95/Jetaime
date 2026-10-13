package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const actionReviewPrompt = basePrompt + `
任务：独立核对候选行动的证据及截至对话末尾的状态，不生成新行动。候选可能有错误，不能默认接受。只有AI安排不能证明真人承诺；真人明确提醒请求只授权提醒本身。acceptedActions给出已校验的请求/接受关系，核对这些行动的后续状态，不重新猜测接受者身份。
先为每个候选分别确认：行动是什么、是谁的安排、直接安排来源、后续是否有明确针对本项的状态变化。不能把同一段对话里的不同动作当作一个整体状态。
状态证据必须同时对应本项行动和行动主体。完成动作A的消息只能结束A；即使动作B由同一人计划、紧接A发生，也不能用A的完成证据结束B。
“做完A再做B”“A完成后去B”表达执行顺序：当后续确认A完成时，只证明B的前置条件成立，不证明B已经执行或完成。没有针对B的完成、取消或修改证据，且B已形成明确个人安排时，B仍是active。
省略主语的连续个人计划继承该段发言者，属于本人安排，不要求对方再次同意。未收到后续反馈、没看到完成消息、没有精确时间，均不能单独构成uncertain。
对每个候选逐条查阅全部消息，返回一条 review。输出顺序必须是candidateIndex、evidenceMessageIds、reason、status：先选择对应本项的证据，说明是否有本项状态变化，最后决定状态。没有明确的本项完成证据时，不能从执行顺序猜测完成。
- active：有明确本人安排/接受的证据，负责人正确，且后续没有完成/取消，也未在tasks创建。
- completed：后续语句表示该行动已经完成。必须结合口语和引用判断；例如“最后一份，拿下”引用购买计划，表示已经买到，不再需要买。完成一项不代表其他计划也完成。
- cancelled：该行动已取消或被替换。
- uncommitted：只有疑问、建议、愿望、转发通知或指令示例，没有本人明确安排/接受。
- uncertain：存在具体的主体/承诺歧义、相互矛盾的证据，或候选遗漏影响执行的关键条件；reason必须指出该歧义或矛盾。不知道是否已经执行，不等于不知道是否有明确安排。
明确计划在没有后续完成/取消证据时保持 active；不能因为没有完成消息就标 uncertain。本人连续描述自己的计划可构成明确安排，简短省略主语的“做某事”要结合前后本人计划理解，不要求他人接受，更不要求共同承诺。完成一个购买行动并不会完成后续学习等独立行动。
只有 active 可以进入待办建议。问话者不是答复者日程的负责人，引用者不是被引用内容的发送者。候选把负责人归错、把问句当共同承诺或把不同时间行动合并时应标 uncertain，不要替它补证据。
必须覆盖每个 candidateIndex，不能重复或遗漏；evidenceMessageIds引用真实消息（允许引用候选来源以后的完成消息）；reason说明本项安排证据，以及是否找到针对本项的状态变化。completed/cancelled必须有本项明确完成/取消证据，active引用本项安排来源；不要把“缺少后续完成证据”作为uncertain理由。只输出 Schema JSON。`

var actionReviewSchema = json.RawMessage(`{"type":"object","additionalProperties":false,"required":["reviews"],"properties":{"reviews":{"type":"array","minItems":1,"maxItems":10,"items":{"type":"object","additionalProperties":false,"required":["candidateIndex","evidenceMessageIds","reason","status"],"properties":{"candidateIndex":{"type":"integer","minimum":0,"maximum":9},"evidenceMessageIds":{"type":"array","minItems":1,"items":{"type":"string"}},"reason":{"type":"string","minLength":1},"status":{"type":"string","enum":["active","completed","cancelled","uncommitted","uncertain"]}}}}}}`)

type actionReview struct {
	CandidateIndex     int      `json:"candidateIndex"`
	Status             string   `json:"status"`
	EvidenceMessageIDs []string `json:"evidenceMessageIds"`
	Reason             string   `json:"reason"`
}

func filterReviewedActions(candidates []Suggestion, reviews []actionReview, group string, messages map[string]Message) ([]Suggestion, error) {
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
		if err := checkSources(group, review.EvidenceMessageIDs, messages, true); err != nil {
			return nil, err
		}
		keep[i] = review.Status == "active"
	}
	result := []Suggestion{}
	for i, candidate := range candidates {
		if keep[i] {
			result = append(result, candidate)
		}
	}
	return result, nil
}

func (w *Workflow) reviewUnresolvedActions(ctx context.Context, run *Run, input Context, candidates []Suggestion, messages map[string]Message) ([]Suggestion, error) {
	if len(candidates) == 0 {
		return candidates, nil
	}
	payload, err := json.Marshal(struct {
		Context         Context                  `json:"context"`
		Candidates      []Suggestion             `json:"candidates"`
		AcceptedActions []acceptedActionEvidence `json:"acceptedActions"`
	}{input, candidates, acceptanceReviewEvidence(input, candidates)})
	if err != nil {
		return nil, err
	}
	request := ModelRequest{Model: run.Model, Messages: []ChatMessage{{Role: "system", Content: actionReviewPrompt + "\nSchema: " + string(actionReviewSchema)}, {Role: "user", Content: string(payload)}}, Format: actionReviewSchema, Options: ModelOptions{Temperature: 0, NumCtx: w.Tokens, NumPredict: 2048}}
	if len(request.Messages[0].Content)+len(request.Messages[1].Content)+2560 > w.Tokens {
		return nil, fmt.Errorf("行动核对上下文超过容量限制；没有截断证据")
	}
	cacheStart := time.Now()
	key, cacheEnabled, bypassReason := "", false, "provider has no model fingerprint"
	if provider, ok := w.Model.(reviewCacheModel); w.EnableReviewCache && ok {
		identity, identityErr := provider.ReviewCacheIdentity(ctx, run.Model)
		if identityErr == nil && identity != "" {
			key, err = actionReviewCacheKey(identity, input, candidates, request)
			if err != nil {
				return nil, err
			}
			cacheEnabled = true
		} else {
			bypassReason = "model fingerprint unavailable; perform full review"
		}
	}
	content, cacheHit := "", false
	if cacheEnabled {
		content, cacheHit = w.reviewCache.get(key)
	}
	cacheStatus := "bypass"
	if cacheEnabled {
		cacheStatus = "miss"
		if cacheHit {
			cacheStatus = "hit"
		}
	}
	cacheOutput := map[string]any{"status": cacheStatus, "key": key}
	if !cacheEnabled {
		cacheOutput["reason"] = bypassReason
	}
	if w.EnableReviewCache {
		run.Steps = append(run.Steps, Step{Name: "review_cache", Status: "succeeded", StartedAt: cacheStart, DurationNS: time.Since(cacheStart).Nanoseconds(), DurationMS: time.Since(cacheStart).Milliseconds(), Output: cacheOutput})
	}
	output := ModelResult{Content: content}
	if !cacheHit {
		start := time.Now()
		var callErr error
		output, callErr = w.Model.Call(ctx, request)
		step := Step{Name: "review_model_call", Status: "succeeded", StartedAt: start, DurationNS: time.Since(start).Nanoseconds(), DurationMS: time.Since(start).Milliseconds(), Input: request, Output: map[string]any{"rawResponse": output.Raw, "content": output.Content, "promptTokens": output.PromptTokens, "completionTokens": output.CompletionTokens}}
		if callErr != nil {
			step.Status = "failed"
			step.Error = callErr.Error()
		}
		run.Steps = append(run.Steps, step)
		if callErr != nil {
			return nil, callErr
		}
	}
	var response struct {
		Reviews []actionReview `json:"reviews"`
	}
	if err = strictJSON([]byte(output.Content), &response); err != nil {
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
	if cacheEnabled && !cacheHit {
		cacheable := true
		for _, review := range response.Reviews {
			if review.Status == "uncertain" {
				cacheable = false
				break
			}
		}
		// Do not store a verdict if the provider changed weights during inference.
		identity, identityErr := w.Model.(reviewCacheModel).ReviewCacheIdentity(ctx, run.Model)
		currentKey, keyErr := actionReviewCacheKey(identity, input, candidates, request)
		if cacheable && identityErr == nil && keyErr == nil && currentKey == key {
			w.reviewCache.put(key, output.Content)
			cacheOutput["stored"] = true
		} else {
			cacheOutput["stored"] = false
			if !cacheable {
				cacheOutput["reason"] = "uncertain review is not cached"
			}
		}
	}
	run.Steps = append(run.Steps, Step{Name: "review_actions", Status: "succeeded", StartedAt: time.Now(), Output: map[string]any{"reviews": response.Reviews, "candidateCount": len(candidates), "keptCount": len(filtered), "cacheHit": cacheHit, "semanticAccuracy": "model judgment; requires human review"}})
	return filtered, nil
}

func (w *Workflow) reviewActions(ctx context.Context, run *Run, input Context, candidates []Suggestion, messages map[string]Message) ([]Suggestion, error) {
	// Once an action is accepted at the end of the supplied history there is no
	// later state transition to infer. Keep that verified acceptance directly,
	// unless an existing task still requires duplicate review.
	active := map[string]bool{}
	unresolved := []Suggestion{}
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
		run.Steps = append(run.Steps, Step{Name: "terminal_acceptance_state", Status: "succeeded", StartedAt: time.Now(), Output: map[string]any{"acceptedAsActiveCount": len(active), "reason": "verified request/acceptance pair ends supplied history; no later transition or existing duplicate"}})
	}
	reviewed, err := w.reviewUnresolvedActions(ctx, run, input, unresolved, messages)
	if err != nil {
		return nil, err
	}
	for _, candidate := range reviewed {
		active[candidate.ID] = true
	}
	result := []Suggestion{}
	for _, candidate := range candidates {
		if active[candidate.ID] {
			result = append(result, candidate)
		}
	}
	return result, nil
}
