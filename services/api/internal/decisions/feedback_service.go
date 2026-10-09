package decisions

import (
	"context"
	"fmt"
	"slices"
)

// Feedback 校验访问权限、应用单个反馈事件，再一起保存历史和会话状态。
// 操作人来自可信调用身份，事件时间由服务端时钟填写。
func (s *DecisionService) Feedback(ctx context.Context, feedback Feedback, actorID string) (Session, error) {
	if err := ctx.Err(); err != nil {
		return Session{}, err
	}
	if feedback.Action == FeedbackChangeBatch {
		return Session{}, fmt.Errorf("use ChangeBatch for retryable generation")
	}
	if feedback.UserID != "" && feedback.UserID != actorID {
		return Session{}, ErrForbidden
	}

	// 1. 读取决策与会话，确认操作人当前有访问权限。
	snapshot, err := s.store.loadDecision(feedback.DecisionID, "")
	if err != nil {
		return Session{}, err
	}
	if err := authorizeDecision(snapshot.Data, snapshot.Decision, actorID); err != nil {
		return Session{}, err
	}
	decision, session := snapshot.Decision, snapshot.Session

	// 2. 新的采纳操作需重查事实；旧事件重试不覆盖后来的选择。
	retry := hasFeedback(decision, feedback.ID)
	feedback.UserID, feedback.CreatedAt = actorID, s.now()
	if feedback.Action == FeedbackAdopt && !retry && s.now().Before(session.ExpiresAt) {
		if err := s.validateAdoption(ctx, snapshot.Data, decision, session, feedback, actorID); err != nil {
			return Session{}, err
		}
	}

	// 3. 应用反馈规则，再将历史和会话按读取版本一起保存。
	if err := session.ApplyDecisionFeedback(&decision, feedback, actorID, s.now()); err != nil {
		return Session{}, err
	}
	if err := ctx.Err(); err != nil {
		return Session{}, err
	}
	if err := s.store.commitFeedback(decision, session, snapshot.Revision, retry); err != nil {
		return Session{}, err
	}
	return copyValue(session)
}

// ChangeBatch 先登记换批命令再生成：失败可以重试，已完成则返回原结果。
// eventID 表示一次用户操作，同一次操作的网络重试应复用该 ID。
func (s *DecisionService) ChangeBatch(ctx context.Context, decisionID, actorID, eventID string) (Decision, error) {
	if err := ctx.Err(); err != nil {
		return Decision{}, err
	}
	if eventID == "" {
		return Decision{}, fmt.Errorf("batch event ID is required")
	}
	key := decisionID + "\x00" + eventID
	request, completed, err := s.startBatch(decisionID, actorID, eventID, key)
	if err != nil {
		return Decision{}, err
	}
	if completed != nil {
		return *completed, nil
	}

	decision, err := s.generate(ctx, request, actorID, key)
	if err != nil {
		s.store.markBatchFailed(key)
	}
	return decision, err
}

// GetDecision 读取历史副本，并检查操作人当前是否仍有访问权限。
func (s *DecisionService) GetDecision(decisionID, actorID string) (Decision, error) {
	snapshot, err := s.store.loadDecision(decisionID, "")
	if err != nil {
		return Decision{}, err
	}
	if err := authorizeDecision(snapshot.Data, snapshot.Decision, actorID); err != nil {
		return Decision{}, err
	}
	return snapshot.Decision, nil
}

// GetSession 读取会话副本，并根据当前空间和最新决策校验访问权限。
func (s *DecisionService) GetSession(sessionID, actorID string) (Session, error) {
	snapshot, err := s.store.loadSession(sessionID)
	if err != nil {
		return Session{}, err
	}
	if _, _, err := scope(snapshot.Data, snapshot.Session.SpaceID, actorID); err != nil {
		return Session{}, err
	}
	if snapshot.Session.LatestDecisionID != "" {
		if err := authorizeDecision(snapshot.Data, snapshot.Decision, actorID); err != nil {
			return Session{}, err
		}
	}
	return snapshot.Session, nil
}

// validateAdoption 在新的采纳事件应用前，重新检查选中笔记的当前硬约束。
func (s *DecisionService) validateAdoption(ctx context.Context, data Dataset, decision Decision, session Session, feedback Feedback, actorID string) error {
	var noteID string
	for _, option := range decision.Result.Options {
		if option.OptionID == feedback.OptionID && option.Selection != nil {
			noteID = option.Selection.NoteID
		}
	}
	// 允许重新采纳曾拒绝的笔记：临时解除排除，随后重新核查事实。
	session.ExcludedNoteIDs = slices.Clone(session.ExcludedNoteIDs)
	session.ExcludedNoteIDs = slices.DeleteFunc(session.ExcludedNoteIDs, func(id string) bool { return id == noteID })
	request := requestFromSession(session)
	input, err := buildContext(data, request, actorID, session)
	if err != nil {
		return err
	}
	evaluation, err := s.evaluateCandidates(ctx, input)
	if err != nil {
		return err
	}
	for _, candidate := range evaluation.Candidates {
		if !evaluation.ConditionsConflict && candidate.NoteID == noteID && candidate.Status == ConstraintSatisfied {
			return nil
		}
	}
	return fmt.Errorf("selected note no longer satisfies current hard constraints")
}

// startBatch 检查换批权限、会话有效性与命令状态，然后登记执行中命令。
// 返回非 nil 的 Decision 代表该命令已经完成，调用方应直接复用结果。
func (s *DecisionService) startBatch(decisionID, actorID, eventID, key string) (Request, *Decision, error) {
	snapshot, err := s.store.loadDecision(decisionID, key)
	if err != nil {
		return Request{}, nil, err
	}
	if err := authorizeDecision(snapshot.Data, snapshot.Decision, actorID); err != nil {
		return Request{}, nil, err
	}
	if command := snapshot.Command; command != nil {
		if command.ActorID != actorID || command.DecisionID != decisionID {
			return Request{}, nil, ErrStateChanged
		}
		if command.ResultID != "" {
			completed, err := s.GetDecision(command.ResultID, actorID)
			return Request{}, &completed, err
		}
		if command.Running {
			return Request{}, nil, ErrBusy
		}
	}
	decision, session := snapshot.Decision, snapshot.Session
	if !s.now().Before(session.ExpiresAt) || session.LatestDecisionID != decisionID {
		return Request{}, nil, ErrStateChanged
	}
	feedback := Feedback{ID: eventID, DecisionID: decisionID, UserID: actorID, Action: FeedbackChangeBatch, CreatedAt: s.now()}
	if err := session.ApplyDecisionFeedback(&decision, feedback, actorID, s.now()); err != nil {
		return Request{}, nil, err
	}
	if err := s.store.commitBatchRequest(decision, session, snapshot.Revision, key, actorID); err != nil {
		return Request{}, nil, err
	}
	return requestFromSession(session), nil, nil
}

// hasFeedback 判断事件是否已记录，防止重试再次影响当前会话状态。
func hasFeedback(decision Decision, eventID string) bool {
	for _, feedback := range decision.Feedback {
		if feedback.ID == eventID {
			return true
		}
	}
	return false
}

// requestFromSession 复用已确认的意图和条件，换一批时不改变原任务。
func requestFromSession(session Session) Request {
	return Request{
		SpaceID: session.SpaceID, RequesterID: session.RequesterID, Task: session.Task,
		Query: session.Query, Conditions: session.Conditions, SessionID: session.ID,
	}
}
