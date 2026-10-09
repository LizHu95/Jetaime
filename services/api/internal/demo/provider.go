package demo

import (
	"context"
	"slices"
	"sort"
	"strings"

	"github.com/LizHu95/Jetaime/services/api/internal/decisions"
	"github.com/LizHu95/Jetaime/services/api/internal/notes"
)

// FixedProvider ranks fictional tags against an explicit preference vocabulary.
// It does not call an LLM, infer facts or inspect private fixture data.
type FixedProvider struct{}

func (FixedProvider) Generate(ctx context.Context, input decisions.Context) (decisions.Result, error) {
	if err := ctx.Err(); err != nil {
		return decisions.Result{}, err
	}
	preferences := func(content string) string {
		switch content {
		case "喜欢辣", "推断：喜欢辣":
			return "辣"
		case "喜欢清淡":
			return "清淡"
		case "喜欢安静":
			return "安静"
		case "喜欢户外":
			return "户外"
		case "喜欢室内":
			return "室内"
		case "喜欢喜剧":
			return "喜剧"
		default:
			return ""
		}
	}
	score := func(n notes.Note) int {
		value := 0
		for _, p := range input.Participants {
			for _, m := range p.RelevantMemories {
				tag := preferences(m.Content)
				if tag != "" && slices.Contains(n.Tags, tag) {
					value++
				}
			}
		}
		for _, preference := range input.Conditions.SoftPreferences {
			if slices.Contains(n.Tags, preference) {
				value++
			}
		}
		return value
	}
	candidates := slices.Clone(input.Candidates)
	sort.SliceStable(candidates, func(i, j int) bool { return score(candidates[i]) > score(candidates[j]) })
	result := decisions.Result{Outcome: decisions.OutcomeRecommended, Explanation: "固定 Mock：先通过硬约束，再按已授权偏好与虚构标签匹配排序。", Options: []decisions.Option{}}
	for _, n := range candidates {
		if len(result.Options) == decisions.MaxSelectRecommendations {
			break
		}
		option := decisions.Option{Title: n.Title, Selection: &decisions.Selection{NoteID: n.ID}, Reason: "已验证满足本次适用硬约束；来源为当前空间收藏的虚构资料。", ParticipantMatches: []decisions.ParticipantMatch{}, Unknowns: []string{"营业状态、路线和可预订情况未接入实时信息"}}
		for _, p := range input.Participants {
			var matches []string
			for _, m := range p.RelevantMemories {
				tag := preferences(m.Content)
				if tag != "" && slices.Contains(n.Tags, tag) {
					matches = append(matches, tag)
				}
			}
			explanation := "满足适用硬约束；没有命中当前支持的软偏好标签。"
			if len(matches) > 0 {
				explanation = "满足适用硬约束；匹配已授权偏好：" + strings.Join(matches, "、") + "。"
			}
			option.ParticipantMatches = append(option.ParticipantMatches, decisions.ParticipantMatch{UserID: p.UserID, Explanation: explanation})
		}
		result.Options = append(result.Options, option)
	}
	return result, nil
}
