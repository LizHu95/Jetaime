package decisions

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"
)

// ValidateSelect 校验本期请求的任务类型、必填身份与意图，以及条件是否合法。
func (r Request) ValidateSelect() error {
	if r.Task != TaskSelect {
		return fmt.Errorf("unsupported task %q", r.Task)
	}
	if strings.TrimSpace(r.SpaceID) == "" || strings.TrimSpace(r.RequesterID) == "" || strings.TrimSpace(r.Query) == "" {
		return fmt.Errorf("request space, requester and query are required")
	}
	return r.Conditions.ValidateSelect()
}

// ValidateSelect 拒绝规划参数、负数限制和不支持的笔记类型。
func (c Conditions) ValidateSelect() error {
	if c.Planning != nil {
		return fmt.Errorf("select does not accept planning conditions")
	}
	if c.BudgetMaxCents != nil && *c.BudgetMaxCents < 0 || c.DurationMaxMinutes != nil && *c.DurationMaxMinutes < 0 {
		return fmt.Errorf("budget and duration cannot be negative")
	}
	for _, t := range c.NoteTypes {
		if !t.IsValid() {
			return fmt.Errorf("invalid note filter %q", t)
		}
	}
	return nil
}

// ValidateSelect 按可信 Context 检查结果结构、候选引用与参与者解释是否完整。
// 硬约束是否满足由事实检查负责，当前访问权限由服务层按空间成员数据检查。
func (r Result) ValidateSelect(ctx Context) error {
	if ctx.Task != TaskSelect {
		return fmt.Errorf("unsupported task %q", ctx.Task)
	}
	if err := ctx.Conditions.ValidateSelect(); err != nil {
		return err
	}
	if strings.TrimSpace(r.Explanation) == "" {
		return fmt.Errorf("result explanation is required")
	}
	switch r.Outcome {
	case OutcomeRecommended:
		if len(r.Options) == 0 || len(r.Options) > MaxSelectRecommendations {
			return fmt.Errorf("select must recommend one to three options")
		}
	case OutcomeNoCandidates, OutcomeConstraintConflict, OutcomeInsufficientInfo:
		if len(r.Options) != 0 {
			return fmt.Errorf("non-recommended outcome cannot contain options")
		}
	default:
		return fmt.Errorf("invalid outcome %q", r.Outcome)
	}
	candidates := make(map[string]bool)
	for _, n := range ctx.Candidates {
		if err := n.Validate(); err != nil {
			return err
		}
		if len(ctx.Conditions.NoteTypes) != 0 {
			allowed := false
			for _, t := range ctx.Conditions.NoteTypes {
				allowed = allowed || n.Type == t
			}
			if !allowed {
				return fmt.Errorf("candidate violates note type filter")
			}
		}
		if candidates[n.ID] {
			return fmt.Errorf("duplicate candidate %q", n.ID)
		}
		candidates[n.ID] = true
	}
	participants := make(map[string]bool)
	for _, p := range ctx.Participants {
		if strings.TrimSpace(p.UserID) == "" || participants[p.UserID] {
			return fmt.Errorf("invalid or duplicate participant")
		}
		participants[p.UserID] = true
	}
	if len(participants) == 0 {
		return fmt.Errorf("participants are required")
	}
	if r.Outcome != OutcomeRecommended {
		return nil
	}
	options, selected := make(map[string]bool), make(map[string]bool)
	for _, o := range r.Options {
		if strings.TrimSpace(o.OptionID) == "" || options[o.OptionID] || strings.TrimSpace(o.Title) == "" || strings.TrimSpace(o.Reason) == "" {
			return fmt.Errorf("invalid or duplicate option")
		}
		options[o.OptionID] = true
		if o.Selection == nil || o.Itinerary != nil {
			return fmt.Errorf("select option requires only a selection payload")
		}
		id := o.Selection.NoteID
		if !candidates[id] || selected[id] {
			return fmt.Errorf("invalid or repeated selected note %q", id)
		}
		selected[id] = true
		matched := make(map[string]bool)
		for _, m := range o.ParticipantMatches {
			if !participants[m.UserID] || matched[m.UserID] || strings.TrimSpace(m.Explanation) == "" {
				return fmt.Errorf("invalid participant match")
			}
			matched[m.UserID] = true
		}
		if len(matched) != len(participants) {
			return fmt.Errorf("each participant requires a match explanation")
		}
	}
	return nil
}

// ValidateFor 只允许在意图不变、作用域一致且未过期时继续当前会话。
// 这里进行确定性比较，不调用模型；自然语言的歧义需要在组装请求前解决。
func (s Session) ValidateFor(r Request, now time.Time) error {
	if err := r.ValidateSelect(); err != nil {
		return err
	}
	if s.ID == "" || r.SessionID != s.ID || s.SpaceID != r.SpaceID || s.RequesterID != r.RequesterID || s.Task != r.Task {
		return fmt.Errorf("session scope mismatch")
	}
	if !now.Before(s.ExpiresAt) {
		return fmt.Errorf("session expired")
	}
	if strings.TrimSpace(s.Query) != strings.TrimSpace(r.Query) || !sameConditions(s.Conditions, r.Conditions) {
		return fmt.Errorf("changed intent requires a new session")
	}
	return nil
}

// sameConditions 忽略列表顺序、重复项与文本首尾空白，再比较条件。
// 数值的 nil 与零保留不同含义，不能合并。
func sameConditions(a, b Conditions) bool {
	canonical := func(c Conditions) Conditions {
		c.NoteTypes = slices.Clone(c.NoteTypes)
		slices.Sort(c.NoteTypes)
		c.NoteTypes = slices.Compact(c.NoteTypes)
		if len(c.NoteTypes) == 0 {
			c.NoteTypes = nil
		}
		c.HardConstraints = canonicalStrings(c.HardConstraints)
		c.SoftPreferences = canonicalStrings(c.SoftPreferences)
		return c
	}
	return reflect.DeepEqual(canonical(a), canonical(b))
}

// canonicalStrings 将条件文本去空白、去空项、排序并去重，便于稳定比较。
func canonicalStrings(values []string) []string {
	var result []string
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	slices.Sort(result)
	return slices.Compact(result)
}

// ApplyDecisionFeedback 同时更新决策的反馈历史和会话的当前状态。
// 本方法只修改传入对象；服务层负责当前权限校验，并将两者原子保存。
// 空间中的其他参与者也可反馈，actorID 不一定等于会话发起人。
func (s *Session) ApplyDecisionFeedback(d *Decision, f Feedback, actorID string, now time.Time) error {
	if s == nil || d == nil || s.ID == "" || d.SessionID != s.ID || d.SpaceID != s.SpaceID || d.RequesterID != s.RequesterID || d.Task != s.Task {
		return fmt.Errorf("decision session scope mismatch")
	}
	if strings.TrimSpace(d.Query) != strings.TrimSpace(s.Query) || !sameConditions(d.Conditions, s.Conditions) {
		return fmt.Errorf("decision intent does not match session")
	}
	active := now.Before(s.ExpiresAt)
	for _, old := range d.Feedback {
		if old.ID == f.ID {
			return d.ApplyFeedback(f, actorID)
		}
	}
	if f.Action == FeedbackChangeBatch && (!active || d.ID != s.LatestDecisionID) {
		return fmt.Errorf("change_batch requires the latest active decision")
	}
	var noteID string
	if f.Action == FeedbackAdopt || f.Action == FeedbackReject {
		for _, o := range d.Result.Options {
			if o.OptionID == f.OptionID && o.Selection != nil && o.Itinerary == nil {
				noteID = o.Selection.NoteID
			}
		}
		if noteID == "" {
			return fmt.Errorf("feedback requires a valid select option")
		}
	}
	before := len(d.Feedback)
	if err := d.ApplyFeedback(f, actorID); err != nil {
		return err
	}
	if len(d.Feedback) == before || !active {
		return nil
	}
	if f.Action == FeedbackChangeBatch {
		return nil
	}
	if f.Action == FeedbackAdopt {
		s.AdoptedOption = &OptionRef{DecisionID: d.ID, OptionID: f.OptionID, NoteID: noteID}
		s.ExcludedNoteIDs = slices.DeleteFunc(s.ExcludedNoteIDs, func(id string) bool { return id == noteID })
	} else if f.Action == FeedbackReject {
		if s.AdoptedOption != nil && (s.AdoptedOption.DecisionID == d.ID && s.AdoptedOption.OptionID == f.OptionID || noteID != "" && s.AdoptedOption.NoteID == noteID) {
			s.AdoptedOption = nil
		}
		if noteID != "" && !slices.Contains(s.ExcludedNoteIDs, noteID) {
			s.ExcludedNoteIDs = append(s.ExcludedNoteIDs, noteID)
		}
	}
	return nil
}

// ApplyFeedback 追加反馈事件，用事件 ID 识别重试；同 ID 的内容必须一致。
// 调用前仍需校验当前空间成员资格，actorID 必须来自可信身份。
func (d *Decision) ApplyFeedback(f Feedback, actorID string) error {
	if d == nil || d.Task != TaskSelect {
		return fmt.Errorf("feedback requires a supported decision")
	}
	if d.ID == "" || f.DecisionID != d.ID || f.ID == "" || actorID == "" || f.UserID != actorID || f.CreatedAt.IsZero() {
		return fmt.Errorf("invalid feedback identity")
	}
	participant := false
	for _, id := range d.ParticipantIDs {
		participant = participant || id == actorID
	}
	if !participant || d.Result.Outcome != OutcomeRecommended {
		return fmt.Errorf("feedback requires a participant and recommended result")
	}
	optionExists := false
	for _, o := range d.Result.Options {
		optionExists = optionExists || o.OptionID == f.OptionID
	}
	switch f.Action {
	case FeedbackAdopt, FeedbackReject:
		if f.OptionID == "" || !optionExists {
			return fmt.Errorf("feedback option does not belong to decision")
		}
	case FeedbackChangeBatch:
		if f.OptionID != "" {
			return fmt.Errorf("change_batch cannot target an option")
		}
	default:
		return fmt.Errorf("invalid feedback action")
	}
	for _, old := range d.Feedback {
		if old.ID == f.ID {
			if old.DecisionID == f.DecisionID && old.UserID == f.UserID && old.Action == f.Action && old.OptionID == f.OptionID && old.Reason == f.Reason {
				return nil
			}
			return fmt.Errorf("feedback id reused with different content")
		}
	}
	d.Feedback = append(d.Feedback, f)
	return nil
}

// AdoptedOptionID 按已接受事件的追加顺序，推导本轮决策最后采纳的方案。
// 后一次采纳覆盖前一次，拒绝当前采纳会清除它；不按客户端时间排序。
func (d Decision) AdoptedOptionID() string {
	id := ""
	for _, f := range d.Feedback {
		if f.Action == FeedbackAdopt {
			id = f.OptionID
		} else if f.Action == FeedbackReject && f.OptionID == id {
			id = ""
		}
	}
	return id
}
