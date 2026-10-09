package notes

import (
	"fmt"
	"strings"

	"github.com/LizHu95/Jetaime/services/api/internal/space"
)

func (t Type) IsValid() bool {
	switch t {
	case TypeRestaurant, TypeRecipe, TypeActivity, TypeMovie, TypeOther:
		return true
	default:
		return false
	}
}

func (n Note) Validate() error {
	if strings.TrimSpace(n.ID) == "" || strings.TrimSpace(n.CreatorID) == "" || strings.TrimSpace(n.Title) == "" {
		return fmt.Errorf("note id, creator and title are required")
	}
	if !n.Type.IsValid() {
		return fmt.Errorf("invalid note type %q", n.Type)
	}
	return nil
}

// CanEdit is an ownership check; identity must already be authenticated.
func (n Note) CanEdit(userID string) bool {
	return userID != "" && n.CreatorID == userID
}

// CanCollectIn requires scoped source authorization and current target membership.
// The author source is the only supported source in V1; callers cannot bypass
// source verification by constructing a CollectionAuthorization themselves.
func (n Note) CanCollectIn(actorID string, s space.Space, members []space.Member, authorization CollectionAuthorization) bool {
	return authorization.ValidateFor(n, actorID, s.ID) == nil && isMember(actorID, s, members)
}

// CanReadIn checks an active, authoritative V1 collection and member snapshot.
// Deleted notes/collections must be removed by the repository before this call.
func (sn SpaceNote) CanReadIn(n Note, actorID string, s space.Space, members []space.Member) bool {
	return n.Validate() == nil && sn.SpaceID == s.ID && sn.NoteID == n.ID && sn.AddedBy == n.CreatorID && isMember(actorID, s, members)
}

// CanRemoveIn does not delete the note body or require author ownership.
func (sn SpaceNote) CanRemoveIn(actorID string, s space.Space, members []space.Member) bool {
	return sn.NoteID != "" && sn.SpaceID == s.ID && isMember(actorID, s, members)
}

func isMember(actorID string, s space.Space, members []space.Member) bool {
	if s.ValidateMembers(members) != nil {
		return false
	}
	for _, m := range members {
		if m.UserID == actorID {
			return true
		}
	}
	return false
}
