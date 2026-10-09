package decisions

import (
	"slices"
	"sort"

	"github.com/LizHu95/Jetaime/services/api/internal/memory"
	"github.com/LizHu95/Jetaime/services/api/internal/notes"
	"github.com/LizHu95/Jetaime/services/api/internal/space"
)

// scope 找到目标空间和实际成员，确认 actorID 属于该空间。
// 身份由调用方核验；这里检查空间范围，不通过客户端声明授予成员资格。
func scope(data Dataset, spaceID, actorID string) (space.Space, []space.Member, error) {
	var found *space.Space
	for _, value := range data.Spaces {
		if value.ID == spaceID {
			value := value
			found = &value
			break
		}
	}
	if found == nil {
		return space.Space{}, nil, ErrNotFound
	}
	var members []space.Member
	allowed := false
	for _, m := range data.Members {
		if m.SpaceID == spaceID {
			members = append(members, m)
			allowed = allowed || m.UserID == actorID
		}
	}
	if err := found.ValidateMembers(members); err != nil {
		return space.Space{}, nil, err
	}
	if !allowed {
		return space.Space{}, nil, ErrForbidden
	}
	sort.Slice(members, func(i, j int) bool { return members[i].UserID < members[j].UserID })
	return *found, members, nil
}

// buildContext 将整套资料筛选为本次允许使用的参与人、记忆和候选。
// 当前遍历内存资料；后续数据查询或检索可调整，授权与硬约束获取规则仍需保留。
func buildContext(data Dataset, request Request, actorID string, session Session) (Context, error) {
	sp, members, err := scope(data, request.SpaceID, actorID)
	if err != nil {
		return Context{}, err
	}
	input := Context{Task: request.Task, Query: request.Query, Conditions: request.Conditions, Participants: []Participant{}, Candidates: []notes.Note{}}
	ownerID := ""
	// 本人个人空间可用自己的私有记忆；共同空间必须逐条有空间使用授权。
	if sp.Type == space.TypePersonal {
		ownerID = sp.CreatorID
	}
	for _, member := range members {
		// 适用硬约束全部获取；其他记忆只作相关偏好参考。
		p := Participant{UserID: member.UserID, HardConstraints: []memory.Memory{}, RelevantMemories: []memory.Memory{}}
		for _, m := range data.Memories {
			if m.UserID != member.UserID || !m.CanUseIn(sp.ID, ownerID) {
				continue
			}
			if m.Strength == memory.StrengthHard {
				p.HardConstraints = append(p.HardConstraints, m)
			} else {
				p.RelevantMemories = append(p.RelevantMemories, m)
			}
		}
		input.Participants = append(input.Participants, p)
	}
	seen := map[string]bool{}
	// 候选需同时满足当前空间收藏、正文读取授权、类型和临时排除条件。
	// seen 去除重复笔记，防止同一 Note 作为多个候选进入生成。
	for _, collection := range data.Collections {
		if collection.SpaceID != sp.ID {
			continue
		}
		for _, n := range data.Notes {
			if n.ID != collection.NoteID || seen[n.ID] || !collection.CanReadIn(n, actorID, sp, members) || slices.Contains(session.ExcludedNoteIDs, n.ID) {
				continue
			}
			if len(request.Conditions.NoteTypes) > 0 && !slices.Contains(request.Conditions.NoteTypes, n.Type) {
				continue
			}
			input.Candidates = append(input.Candidates, n)
			seen[n.ID] = true
		}
	}
	sort.Slice(input.Candidates, func(i, j int) bool { return input.Candidates[i].ID < input.Candidates[j].ID })
	return input, nil
}

// authorizeDecision 保守限制历史访问：实际参与人仍属于空间，并且候选仍可读取。
// 当前资料不可变；未来授权撤回后的历史脱敏规则需要在存储阶段落实。
func authorizeDecision(data Dataset, d Decision, actorID string) error {
	sp, members, err := scope(data, d.SpaceID, actorID)
	if err != nil {
		return err
	}
	if !slices.Contains(d.ParticipantIDs, actorID) {
		return ErrForbidden
	}
	for _, old := range d.CandidateNotes {
		allowed := false
		for _, c := range data.Collections {
			for _, n := range data.Notes {
				if n.ID == old.ID && c.CanReadIn(n, actorID, sp, members) {
					allowed = true
				}
			}
		}
		if !allowed {
			return ErrForbidden
		}
	}
	return nil
}
