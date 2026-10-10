package demo

import (
	"context"
	"fmt"

	"github.com/LizHu95/Jetaime/services/api/internal/decisions"
)

// Checker verifies only explicit numeric limits against sourced facts.
// Natural-language conditions and memories remain in Context for the provider.
type Checker struct{ Facts map[string]Fact }

func (c Checker) Evaluate(ctx context.Context, input decisions.Context) (decisions.SelectEvaluation, error) {
	if err := ctx.Err(); err != nil {
		return decisions.SelectEvaluation{}, err
	}
	if len(input.Participants) == 0 {
		return decisions.SelectEvaluation{}, fmt.Errorf("fact checking requires participants")
	}
	e := decisions.SelectEvaluation{Candidates: []decisions.CandidateAssessment{}, VerifiedFacts: map[string]decisions.NumericFact{}}
	for _, n := range input.Candidates {
		fact, exists := c.Facts[n.ID]
		verified := exists && fact.Verified && fact.Source != ""
		if verified {
			numeric := decisions.NumericFact{Source: fact.Source, People: fact.People}
			if fact.PerPersonCostCents != nil && *fact.PerPersonCostCents >= 0 {
				numeric.PerPersonCostCents = fact.PerPersonCostCents
			}
			if fact.TotalCostCents != nil && *fact.TotalCostCents >= 0 {
				numeric.TotalCostCents = fact.TotalCostCents
			}
			if fact.DurationMinutes != nil && *fact.DurationMinutes >= 0 {
				numeric.DurationMinutes = fact.DurationMinutes
			}
			e.VerifiedFacts[n.ID] = numeric
		}
		status := decisions.ConstraintSatisfied
		merge := func(check decisions.ConstraintStatus) {
			if check == decisions.ConstraintViolated || status == decisions.ConstraintViolated {
				status = decisions.ConstraintViolated
			} else if check == decisions.ConstraintUnknown {
				status = decisions.ConstraintUnknown
			}
		}
		if limit := input.Conditions.BudgetMaxCents; limit != nil {
			if !verified {
				merge(decisions.ConstraintUnknown)
			} else if fact.PerPersonCostCents != nil && *fact.PerPersonCostCents >= 0 {
				// Avoid multiplication overflow; all inputs are nonnegative cents.
				if *fact.PerPersonCostCents > *limit/int64(len(input.Participants)) {
					merge(decisions.ConstraintViolated)
				}
			} else if fact.TotalCostCents != nil && *fact.TotalCostCents >= 0 && fact.People == len(input.Participants) {
				if *fact.TotalCostCents > *limit {
					merge(decisions.ConstraintViolated)
				}
			} else {
				merge(decisions.ConstraintUnknown)
			}
		}
		if limit := input.Conditions.DurationMaxMinutes; limit != nil {
			if !verified || fact.DurationMinutes == nil || *fact.DurationMinutes < 0 {
				merge(decisions.ConstraintUnknown)
			} else if *fact.DurationMinutes > *limit {
				merge(decisions.ConstraintViolated)
			}
		}
		e.Candidates = append(e.Candidates, decisions.CandidateAssessment{NoteID: n.ID, Status: status})
	}
	return e, nil
}
