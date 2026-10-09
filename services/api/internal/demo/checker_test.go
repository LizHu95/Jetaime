package demo

import (
	"context"
	"testing"

	"github.com/LizHu95/Jetaime/services/api/internal/decisions"
	"github.com/LizHu95/Jetaime/services/api/internal/notes"
)

func TestVerifiedCostsAndEvidence(t *testing.T) {
	budget := int64(15000)
	input := decisions.Context{Task: decisions.TaskSelect, Participants: []decisions.Participant{{UserID: UserA}, {UserID: UserB}}, Candidates: []notes.Note{{ID: "n", Type: notes.TypeRestaurant}}, Conditions: decisions.Conditions{BudgetMaxCents: &budget}}
	cases := []struct {
		name        string
		fact        Fact
		constraints []string
		want        decisions.ConstraintStatus
	}{
		{"per person exceeds total", Fact{Verified: true, Source: "fixture", PerPersonCostCents: pointer(int64(8000))}, nil, decisions.ConstraintViolated},
		{"total quote matches group", Fact{Verified: true, Source: "fixture", TotalCostCents: pointer(int64(14000)), People: 2}, nil, decisions.ConstraintSatisfied},
		{"total quote wrong group", Fact{Verified: true, Source: "fixture", TotalCostCents: pointer(int64(14000)), People: 1}, nil, decisions.ConstraintUnknown},
		{"unverified price", Fact{Verified: false, Source: "model", PerPersonCostCents: pointer(int64(100))}, nil, decisions.ConstraintUnknown},
		{"negative price", Fact{Verified: true, Source: "fixture", PerPersonCostCents: pointer(int64(-1))}, nil, decisions.ConstraintUnknown},
		{"known peanut even incomplete ingredients", Fact{Verified: true, Source: "fixture", PerPersonCostCents: pointer(int64(100)), Ingredients: []string{"花生"}}, []string{"不吃花生"}, decisions.ConstraintViolated},
		{"missing ingredients", Fact{Verified: true, Source: "fixture", PerPersonCostCents: pointer(int64(100))}, []string{"不吃花生"}, decisions.ConstraintUnknown},
		{"unsupported natural language", Fact{Verified: true, Source: "fixture", PerPersonCostCents: pointer(int64(100))}, []string{"我想吃特别的东西"}, decisions.ConstraintUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			current := input
			current.Conditions.HardConstraints = tc.constraints
			evaluation, err := (Checker{Facts: map[string]Fact{"n": tc.fact}}).Evaluate(context.Background(), current)
			if err != nil || evaluation.Candidates[0].Status != tc.want {
				t.Fatal(evaluation, err)
			}
		})
	}
	input.Participants = nil
	if _, err := (Checker{}).Evaluate(context.Background(), input); err == nil {
		t.Fatal("empty participant pool accepted")
	}
}
