package memory

import (
	"fmt"
	"strings"
)

func (m Memory) Validate() error {
	if strings.TrimSpace(m.ID) == "" || strings.TrimSpace(m.UserID) == "" || strings.TrimSpace(m.Content) == "" {
		return fmt.Errorf("memory id, user and content are required")
	}
	switch m.Type {
	case TypePreference, TypeConstraint, TypeFeedback:
	default:
		return fmt.Errorf("invalid memory type %q", m.Type)
	}
	switch m.Strength {
	case StrengthLow, StrengthMedium, StrengthStrong, StrengthHard:
	default:
		return fmt.Errorf("invalid memory strength %q", m.Strength)
	}
	switch m.Source {
	case SourceExplicit, SourceInferred:
	case SourceFeedback:
		if m.Type != TypeFeedback || m.Strength != StrengthLow {
			return fmt.Errorf("raw feedback memory must be feedback/low")
		}
	default:
		return fmt.Errorf("invalid memory source %q", m.Source)
	}
	if m.Strength == StrengthHard && m.Type != TypeConstraint {
		return fmt.Errorf("hard memory must be a constraint")
	}
	seen := make(map[string]bool)
	for _, id := range m.AllowedSpaceIDs {
		if strings.TrimSpace(id) == "" || seen[id] {
			return fmt.Errorf("invalid or duplicate allowed space id")
		}
		seen[id] = true
	}
	return nil
}

func (m Memory) IsActive() bool {
	return m.Validate() == nil && (m.Source != SourceInferred || m.Confirmed)
}

// CanUseIn checks memory consent only. The caller must also establish current
// membership and derive personalOwnerID from a validated personal space.
func (m Memory) CanUseIn(spaceID, personalOwnerID string) bool {
	if !m.IsActive() || spaceID == "" {
		return false
	}
	if personalOwnerID != "" {
		return m.UserID == personalOwnerID
	}
	for _, id := range m.AllowedSpaceIDs {
		if id == spaceID {
			return true
		}
	}
	return false
}
