package decisions

import "fmt"

// ConstraintStatus 汇总某条候选对全部适用硬约束的满足情况。
// 已证实违反优先于信息未知；没有硬约束时视为满足。
// 此结论来自服务端事实检查，不接受模型或客户端自行声明。
type ConstraintStatus string

const (
	ConstraintSatisfied ConstraintStatus = "satisfied"
	ConstraintViolated  ConstraintStatus = "violated"
	ConstraintUnknown   ConstraintStatus = "unknown"
)

// CandidateAssessment 将一条候选笔记与它的硬约束检查结论关联。
type CandidateAssessment struct {
	NoteID string
	Status ConstraintStatus
}

// SelectEvaluation 覆盖权限、类型与排除过滤之后的完整候选池。
// 仅检查向量检索的 Top K，无法证明整个候选池为空或没有可用方案。
type SelectEvaluation struct {
	ConditionsConflict bool // 条件本身互相矛盾，与某条笔记是否满足无关。
	Candidates         []CandidateAssessment
}

// Outcome 按优先级得出结论：条件矛盾、无候选、有满足项、信息未知、全部违反。
func (e SelectEvaluation) Outcome() (Outcome, error) {
	seen := make(map[string]bool)
	passes, unknowns := 0, 0
	for _, a := range e.Candidates {
		if a.NoteID == "" || seen[a.NoteID] {
			return "", fmt.Errorf("invalid or duplicate assessment")
		}
		seen[a.NoteID] = true
		switch a.Status {
		case ConstraintSatisfied:
			passes++
		case ConstraintUnknown:
			unknowns++
		case ConstraintViolated:
		default:
			return "", fmt.Errorf("invalid constraint status")
		}
	}
	if e.ConditionsConflict {
		return OutcomeConstraintConflict, nil
	}
	if len(e.Candidates) == 0 {
		return OutcomeNoCandidates, nil
	}
	if passes > 0 {
		return OutcomeRecommended, nil
	}
	if unknowns > 0 {
		return OutcomeInsufficientInfo, nil
	}
	return OutcomeConstraintConflict, nil
}

// ValidateResult 在结构校验之上，再核对业务结论与选中笔记的可信检查结果。
// 提供给模型的 Context 候选可以是完整检查池中的一个子集。
func (e SelectEvaluation) ValidateResult(ctx Context, result Result) error {
	if err := result.ValidateSelect(ctx); err != nil {
		return err
	}
	outcome, err := e.Outcome()
	if err != nil {
		return err
	}
	if result.Outcome != outcome {
		return fmt.Errorf("result outcome differs from server evaluation")
	}
	statuses := make(map[string]ConstraintStatus)
	for _, a := range e.Candidates {
		statuses[a.NoteID] = a.Status
	}
	for _, n := range ctx.Candidates {
		if _, ok := statuses[n.ID]; !ok {
			return fmt.Errorf("context candidate is outside evaluated pool")
		}
	}
	for _, o := range result.Options {
		if statuses[o.Selection.NoteID] != ConstraintSatisfied {
			return fmt.Errorf("selected candidate has violated or unknown hard constraints")
		}
	}
	return nil
}
