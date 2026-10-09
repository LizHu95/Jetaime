package notes

import (
	"testing"

	"github.com/LizHu95/Jetaime/services/api/internal/space"
)

func TestCollectionAuthorizationCannotBeReusedOrForged(t *testing.T) {
	n := Note{ID: "n", CreatorID: "a", Title: "Restaurant", Type: TypeRestaurant}
	s := space.Space{ID: "s", CreatorID: "a", Name: "Mine", Type: space.TypePersonal}
	members := []space.Member{{SpaceID: s.ID, UserID: "a", Role: space.RoleOwner}}
	grant, err := n.AuthorizeAuthorCollection("a", s.ID)
	if err != nil || !n.CanCollectIn("a", s, members, grant) {
		t.Fatal("valid author source rejected", err)
	}
	cases := []struct {
		name   string
		mutate func(*CollectionAuthorization)
	}{
		{"missing source", func(a *CollectionAuthorization) { a.Source = "" }},
		{"future share source", func(a *CollectionAuthorization) { a.Source = "share" }},
		{"future publication source", func(a *CollectionAuthorization) { a.Source = "publication" }},
		{"another note", func(a *CollectionAuthorization) { a.NoteID = "other" }},
		{"another user", func(a *CollectionAuthorization) { a.UserID = "b" }},
		{"another space", func(a *CollectionAuthorization) { a.TargetSpaceID = "other" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := grant
			tc.mutate(&a)
			if n.CanCollectIn("a", s, members, a) {
				t.Fatal("invalid grant accepted")
			}
		})
	}
	if _, err := n.AuthorizeAuthorCollection("b", s.ID); err == nil {
		t.Fatal("non-author issued author grant")
	}
	forged := CollectionAuthorization{Source: CollectionSourceAuthor, NoteID: n.ID, UserID: "b", TargetSpaceID: "shared"}
	if forged.ValidateFor(n, "b", "shared") == nil {
		t.Fatal("scope-matching forged author grant accepted")
	}
	if _, err := n.AuthorizeAuthorCollection("a", " "); err == nil {
		t.Fatal("blank target accepted")
	}
	if n.CanCollectIn("a", s, nil, grant) {
		t.Fatal("grant bypassed live membership")
	}
	changed := n
	changed.CreatorID = "b"
	if changed.CanCollectIn("a", s, members, grant) {
		t.Fatal("stale ownership bypassed source validation")
	}
}
