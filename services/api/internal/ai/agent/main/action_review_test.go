package mainagent

import (
	"testing"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
)

func TestActionReviewFiltersWithoutChangingCandidates(t *testing.T) {
	candidates := []ai.Suggestion{{Title: "已完成"}, {Title: "保留"}, {Title: "邀请"}, {Title: "不确定"}, {Title: "取消"}}
	messages := map[string]ai.Message{"original": {ID: "original", GroupID: "g"}, "later": {ID: "later", GroupID: "g"}}
	reviews := []actionReview{
		{4, "cancelled", []string{"later"}, "状态依据"},
		{0, "completed", []string{"later"}, "状态依据"},
		{2, "uncommitted", []string{"original"}, "状态依据"},
		{1, "active", []string{"original"}, "状态依据"},
		{3, "uncertain", []string{"original"}, "状态依据"},
	}
	got, err := filterReviewedActions(candidates, reviews, "g", messages)
	if err != nil || len(got) != 1 || got[0].Title != "保留" || len(candidates) != 5 {
		t.Fatalf("unexpected filtering: %+v %v", got, err)
	}
}

func TestActionReviewRejectsUnverifiableReview(t *testing.T) {
	candidates := []ai.Suggestion{{Title: "一"}, {Title: "二"}}
	messages := map[string]ai.Message{"a": {ID: "a", GroupID: "g"}, "other": {ID: "other", GroupID: "other"}}
	valid := actionReview{0, "active", []string{"a"}, "状态依据"}
	for name, reviews := range map[string][]actionReview{
		"missing": {valid}, "duplicate": {valid, valid},
		"unknown source":   {valid, {1, "active", []string{"unknown"}, "状态依据"}},
		"cross group":      {valid, {1, "active", []string{"other"}, "状态依据"}},
		"unknown status":   {valid, {1, "guess", []string{"a"}, "状态依据"}},
		"missing evidence": {valid, {1, "active", nil, "状态依据"}},
		"out of range":     {valid, {2, "active", []string{"a"}, "状态依据"}},
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := filterReviewedActions(candidates, reviews, "g", messages); err == nil || got != nil {
				t.Fatalf("unverified suggestions accepted: %+v %v", got, err)
			}
		})
	}
}
