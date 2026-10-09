package notes

import (
	"fmt"
	"strings"
)

// AuthorizeAuthorCollection derives the supported source from an authoritative
// note and authenticated actor. Target membership is checked by CanCollectIn.
func (n Note) AuthorizeAuthorCollection(actorID, targetSpaceID string) (CollectionAuthorization, error) {
	a := CollectionAuthorization{
		Source: CollectionSourceAuthor, NoteID: n.ID,
		UserID: actorID, TargetSpaceID: targetSpaceID,
	}
	if err := a.ValidateFor(n, actorID, targetSpaceID); err != nil {
		return CollectionAuthorization{}, err
	}
	return a, nil
}

// ValidateFor checks source permission as well as scope. Future share or
// publication sources require authoritative grant checks before being accepted.
func (a CollectionAuthorization) ValidateFor(n Note, actorID, targetSpaceID string) error {
	if err := n.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(actorID) == "" || strings.TrimSpace(targetSpaceID) == "" || a.NoteID != n.ID || a.UserID != actorID || a.TargetSpaceID != targetSpaceID {
		return fmt.Errorf("collection authorization scope mismatch")
	}
	switch a.Source {
	case CollectionSourceAuthor:
		if !n.CanEdit(actorID) {
			return fmt.Errorf("author collection requires note ownership")
		}
	default:
		return fmt.Errorf("unsupported collection source %q", a.Source)
	}
	return nil
}
