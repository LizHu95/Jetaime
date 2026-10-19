package mainagent

import (
	"strings"
	"unicode"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
)

// AI identities are recognized only by explicit labels in the supplied fixture.
// A name containing the letters AI alone is not an identity declaration.
func aiMemberIDs(users []ai.User) map[string]bool {
	ids := map[string]bool{}
	for _, user := range users {
		name := strings.ToLower(strings.TrimSpace(user.Nickname))
		if strings.HasSuffix(name, "（ai）") || strings.HasSuffix(name, "(ai)") {
			ids[user.ID] = true
		}
	}
	return ids
}

func compactEvidenceText(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, text)
}

func reminderRequest(text string) (string, bool) {
	text = strings.TrimSpace(text)
	text = strings.TrimPrefix(text, "请")
	if !strings.HasPrefix(text, "提醒我") {
		return "", false
	}
	target := strings.TrimSpace(strings.TrimPrefix(text, "提醒我"))
	if at := strings.Index(target, "@"); at >= 0 {
		target = target[:at]
	}
	target = strings.Trim(target, " \n\t。！!，,；;")
	if target == "" {
		return "", false
	}
	return target, true
}

// Apply conservative source boundaries after model review. Model agreement
// cannot turn AI-only sources into a human commitment or expand a reminder.
func enforceEvidenceBoundaries(input ai.Context, suggestions []ai.Suggestion) ([]ai.Suggestion, []string) {
	aiMembers := aiMemberIDs(input.Users)
	messages := map[string]ai.Message{}
	for _, m := range input.Messages {
		messages[m.ID] = m
	}
	kept := []ai.Suggestion{}
	changes := []string{}
	for _, s := range suggestions {
		humanSources := []ai.Message{}
		for _, id := range s.SourceMessageIDs {
			m := messages[id]
			if !aiMembers[m.SenderID] {
				humanSources = append(humanSources, m)
			}
		}
		if len(humanSources) == 0 {
			changes = append(changes, s.ID+": rejected AI-only action evidence")
			continue
		}
		if len(humanSources) == 1 {
			if target, ok := reminderRequest(humanSources[0].Text); ok {
				if !strings.Contains(s.Title, "提醒") {
					changes = append(changes, s.ID+": rejected expansion of reminder request")
					continue
				}
				s.Title = "提醒" + target
				// No human accepted responsibility; the requester is the beneficiary,
				// not necessarily the person carrying out the reminder.
				s.AssigneeID = nil
				s.SourceMessageIDs = []string{humanSources[0].ID}
				if !strings.Contains(compactEvidenceText(humanSources[0].Text), compactEvidenceText(s.TimeExpression)) {
					s.TimeExpression = ""
					s.DueAt = nil
				}
				s.PossibleDuplicateIDs = nil
				for _, task := range input.Tasks {
					if sameSources(task.SourceMessageIDs, s.SourceMessageIDs) || strings.TrimSpace(task.Title) == s.Title {
						s.PossibleDuplicateIDs = append(s.PossibleDuplicateIDs, task.ID)
					}
				}
				changes = append(changes, s.ID+": bounded reminder to original human request")
			}
		}
		duplicate := false
		for _, previous := range kept {
			if previous.Title == s.Title && previous.TimeExpression == s.TimeExpression && sameSources(previous.SourceMessageIDs, s.SourceMessageIDs) && ((previous.AssigneeID == nil && s.AssigneeID == nil) || (previous.AssigneeID != nil && s.AssigneeID != nil && *previous.AssigneeID == *s.AssigneeID)) {
				duplicate = true
			}
		}
		if !duplicate {
			kept = append(kept, s)
		}
	}
	return kept, changes
}

// A direct reminder request is already explicit authorization for the reminder,
// unlike a forwarded notice or an assistant's proposed execution steps.
func supplementReminders(input ai.Context, candidates []ai.Suggestion) []ai.Suggestion {
	aiMembers := aiMemberIDs(input.Users)
	for _, message := range input.Messages {
		if aiMembers[message.SenderID] {
			continue
		}
		target, ok := reminderRequest(message.Text)
		if !ok {
			continue
		}
		covered := false
		for _, candidate := range candidates {
			if strings.Contains(candidate.Title, "提醒") {
				for _, id := range candidate.SourceMessageIDs {
					if id == message.ID {
						covered = true
					}
				}
			}
		}
		if !covered {
			candidates = append(candidates, ai.Suggestion{Title: "提醒" + target, SourceMessageIDs: []string{message.ID}})
		}
	}
	return candidates
}
