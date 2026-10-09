package decisions

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"slices"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/notes"
)

// prepareSession 创建新会话，或验证已有会话的身份、条件与有效期。
// existing 来自存储副本，返回的会话先参与计算，最终成功后再保存。
func (s *DecisionService) prepareSession(request Request, existing *Session) (Session, error) {
	if existing != nil {
		if err := existing.ValidateFor(request, s.now()); err != nil {
			return Session{}, err
		}
		return *existing, nil
	}
	id, err := identity("session-")
	if err != nil {
		return Session{}, err
	}
	return Session{
		ID: id, SpaceID: request.SpaceID, RequesterID: request.RequesterID,
		Task: request.Task, Query: request.Query, Conditions: request.Conditions,
		RecommendedNoteIDs: []string{}, ExcludedNoteIDs: []string{}, ExpiresAt: s.now().Add(s.ttl),
	}, nil
}

// evaluateCandidates 调用事实检查器，并确保每个合法候选都得到评估。
func (s *DecisionService) evaluateCandidates(ctx context.Context, input Context) (SelectEvaluation, error) {
	// 传入副本，避免外部实现修改后续校验所依赖的原始上下文。
	owned, err := copyValue(input)
	if err != nil {
		return SelectEvaluation{}, err
	}
	evaluation, err := s.checker.Evaluate(ctx, owned)
	if err != nil {
		return SelectEvaluation{}, err
	}
	if err := validateCoverage(input, evaluation); err != nil {
		return SelectEvaluation{}, err
	}
	return evaluation, nil
}

// generateResult 先确定业务结果；只有存在满足项时才调用 Provider。
// 无候选、冲突或必要事实未知时，直接返回空选项和相应说明。
func (s *DecisionService) generateResult(ctx context.Context, input Context, evaluation SelectEvaluation, session Session) (Result, error) {
	outcome, err := evaluation.Outcome()
	if err != nil {
		return Result{}, err
	}
	result := Result{Outcome: outcome, Options: []Option{}, Explanation: outcomeExplanation(outcome)}
	if outcome == OutcomeRecommended {
		input.Candidates = recommendationCandidates(input.Candidates, evaluation, session.RecommendedNoteIDs)
		owned, err := copyValue(input)
		if err != nil {
			return Result{}, err
		}
		result, err = s.provider.Generate(ctx, owned)
		if err != nil {
			return Result{}, err
		}
		for i := range result.Options {
			// 选项标识由服务端生成，不信任模型或其他 Provider 提供的标识。
			result.Options[i].OptionID, err = identity("option-")
			if err != nil {
				return Result{}, err
			}
			if result.Options[i].Unknowns == nil {
				result.Options[i].Unknowns = []string{}
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	// 同时检查结构、候选引用与硬约束状态，解释文字不能代替事实检查。
	if err := evaluation.ValidateResult(input, result); err != nil {
		return Result{}, err
	}
	return result, nil
}

// recommendationCandidates 只保留硬约束满足项，再优先提供未看过的笔记。
// 未看项不足三个时少返回；全部看过后才复用已看项，不复活明确拒绝的笔记。
func recommendationCandidates(candidates []notes.Note, evaluation SelectEvaluation, seen []string) []notes.Note {
	eligible := map[string]bool{}
	for _, candidate := range evaluation.Candidates {
		eligible[candidate.NoteID] = candidate.Status == ConstraintSatisfied
	}
	var safe, unseen []notes.Note
	for _, note := range candidates {
		if !eligible[note.ID] {
			continue
		}
		safe = append(safe, note)
		if !slices.Contains(seen, note.ID) {
			unseen = append(unseen, note)
		}
	}
	if len(unseen) > 0 {
		return unseen
	}
	return safe
}

// newDecision 构建本批次历史记录，保留当时需求、参与人、候选正文和结果。
// 完整私人记忆和模型 Prompt 不直接写入 Decision；存储提交时再做深拷贝。
func newDecision(request Request, input Context, session Session, result Result, now time.Time) (Decision, error) {
	id, err := identity("decision-")
	if err != nil {
		return Decision{}, err
	}
	participants := make([]string, 0, len(input.Participants))
	for _, participant := range input.Participants {
		participants = append(participants, participant.UserID)
	}
	return Decision{
		ID: id, SessionID: session.ID, SpaceID: request.SpaceID, RequesterID: request.RequesterID,
		Task: request.Task, ParticipantIDs: participants, Query: request.Query,
		Conditions: request.Conditions, CandidateNotes: input.Candidates, Result: result,
		Feedback: []Feedback{}, CreatedAt: now,
	}, nil
}

// rememberRecommendation 记录已展示的笔记，供下一批减少重复；已看不等于排除。
func (s *Session) rememberRecommendation(noteID string) {
	if !slices.Contains(s.RecommendedNoteIDs, noteID) {
		s.RecommendedNoteIDs = append(s.RecommendedNoteIDs, noteID)
	}
}

// validateCoverage 防止漏评估、重复评估或评估越界，避免把漏召当成无候选。
func validateCoverage(input Context, evaluation SelectEvaluation) error {
	if _, err := evaluation.Outcome(); err != nil {
		return err
	}
	if len(input.Candidates) != len(evaluation.Candidates) {
		return fmt.Errorf("fact evaluation must cover the complete candidate pool")
	}
	ids := map[string]bool{}
	for _, n := range input.Candidates {
		ids[n.ID] = true
	}
	for _, a := range evaluation.Candidates {
		if !ids[a.NoteID] {
			return fmt.Errorf("evaluation contains a foreign candidate")
		}
	}
	return nil
}

// outcomeExplanation 提供服务端判定对应的基础说明，非推荐状态无需调用模型。
func outcomeExplanation(outcome Outcome) string {
	switch outcome {
	case OutcomeNoCandidates:
		return "当前空间、类型和临时排除条件下没有合法候选，请补充收藏或开启新需求。"
	case OutcomeConstraintConflict:
		return "已确认条件存在冲突，或所有候选都有明确硬约束违反；请调整条件或补充收藏。"
	case OutcomeInsufficientInfo:
		return "没有已验证满足全部硬约束的候选，必要事实未知，请补充费用、时长或成分信息。"
	default:
		return "从已验证满足全部适用硬约束的候选中选择。"
	}
}

// identity 生成带用途前缀的随机标识，分别标识会话、批次和选项。
func identity(prefix string) (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(value[:]), nil
}
