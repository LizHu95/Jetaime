package demo

import (
	"context"
	"fmt"
	"slices"

	"github.com/LizHu95/Jetaime/services/api/internal/decisions"
	"github.com/LizHu95/Jetaime/services/api/internal/notes"
)

// Checker supports an explicit, small vocabulary. Unsupported natural-language
// constraints stay unknown; this is not a production dietary/semantic checker.
type Checker struct{ Facts map[string]Fact }

func (c Checker) Evaluate(ctx context.Context, input decisions.Context) (decisions.SelectEvaluation, error) {
	if err := ctx.Err(); err != nil {
		return decisions.SelectEvaluation{}, err
	}
	if len(input.Participants) == 0 {
		return decisions.SelectEvaluation{}, fmt.Errorf("fact checking requires participants")
	}
	constraints := slices.Clone(input.Conditions.HardConstraints)
	for _, p := range input.Participants {
		for _, m := range p.HardConstraints {
			constraints = append(constraints, m.Content)
		}
	}
	e := decisions.SelectEvaluation{ConditionsConflict: slices.Contains(constraints, "必须室内") && slices.Contains(constraints, "必须户外"), Candidates: []decisions.CandidateAssessment{}}
	for _, n := range input.Candidates {
		fact, exists := c.Facts[n.ID]
		verified := exists && fact.Verified && fact.Source != ""
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
		for _, constraint := range constraints {
			switch constraint {
			case "不吃花生", "不吃海鲜":
				if n.Type != notes.TypeRestaurant && n.Type != notes.TypeRecipe {
					continue
				}
				ingredient := "花生"
				if constraint == "不吃海鲜" {
					ingredient = "海鲜"
				}
				if !verified {
					merge(decisions.ConstraintUnknown)
				} else if slices.Contains(fact.Ingredients, ingredient) {
					merge(decisions.ConstraintViolated)
				} else if !fact.IngredientsKnown {
					merge(decisions.ConstraintUnknown)
				}
			case "必须室内", "必须户外":
				if !verified || fact.Indoor == nil {
					merge(decisions.ConstraintUnknown)
				} else if *fact.Indoor != (constraint == "必须室内") {
					merge(decisions.ConstraintViolated)
				}
			case "必须无障碍":
				if !verified || fact.Accessible == nil {
					merge(decisions.ConstraintUnknown)
				} else if !*fact.Accessible {
					merge(decisions.ConstraintViolated)
				}
			default:
				merge(decisions.ConstraintUnknown)
			}
		}
		e.Candidates = append(e.Candidates, decisions.CandidateAssessment{NoteID: n.ID, Status: status})
	}
	return e, nil
}
