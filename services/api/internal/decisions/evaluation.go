package decisions

import "fmt"

// ConstraintStatus summarizes ALL applicable hard constraints for one candidate.
// Any proven violation dominates unknowns; no constraints means satisfied.
// These assessments come from the server fact checker, never model/client claims.
type ConstraintStatus string

const (
	ConstraintSatisfied ConstraintStatus = "satisfied"
	ConstraintViolated  ConstraintStatus = "violated"
	ConstraintUnknown   ConstraintStatus = "unknown"
)

type CandidateAssessment struct {
	NoteID string
	Status ConstraintStatus
}

// SelectEvaluation covers the complete authorized/filter/exclusion candidate
// pool. Vector Top K alone cannot prove an empty pool or no viable candidate.
type SelectEvaluation struct {
	ConditionsConflict bool
	Candidates         []CandidateAssessment
}

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

// ValidateResult supplements structural validation with trusted fact decisions.
// Context candidates may be a retrieval subset of the full evaluation pool.
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
