package space

import (
	"fmt"
	"strings"
)

// ValidateMembers validates one authoritative space snapshot, not a caller's
// claimed memberships. A user may occur in other spaces independently.
func (s Space) ValidateMembers(members []Member) error {
	if strings.TrimSpace(s.ID) == "" || strings.TrimSpace(s.CreatorID) == "" || strings.TrimSpace(s.Name) == "" {
		return fmt.Errorf("space id, creator and name are required")
	}
	limit := 0
	switch s.Type {
	case TypePersonal:
		limit = 1
	case TypeCouple:
		limit = 2
	default:
		return fmt.Errorf("unsupported space type %q", s.Type)
	}
	if len(members) == 0 || len(members) > limit {
		return fmt.Errorf("invalid member count for %s", s.Type)
	}
	seen := make(map[string]bool)
	owner := false
	for _, m := range members {
		if m.SpaceID != s.ID || strings.TrimSpace(m.UserID) == "" || seen[m.UserID] {
			return fmt.Errorf("invalid or duplicate space member")
		}
		seen[m.UserID] = true
		switch m.Role {
		case RoleOwner:
			if owner || m.UserID != s.CreatorID {
				return fmt.Errorf("space creator must be the sole owner")
			}
			owner = true
		case RoleMember:
		default:
			return fmt.Errorf("invalid member role %q", m.Role)
		}
	}
	if !owner {
		return fmt.Errorf("space creator must be an owner member")
	}
	return nil
}
