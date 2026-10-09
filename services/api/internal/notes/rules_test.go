package notes

import (
	"testing"

	"github.com/LizHu95/Jetaime/services/api/internal/space"
)

func TestCollectionGrantsReadWithoutRedistribution(t *testing.T) {
	s := space.Space{ID: "shared", Type: space.TypeCouple, Name: "Couple", CreatorID: "a"}
	members := []space.Member{{SpaceID: s.ID, UserID: "a", Role: space.RoleOwner}, {SpaceID: s.ID, UserID: "b", Role: space.RoleMember}}
	n := Note{ID: "n", CreatorID: "a", Title: "Restaurant", Type: TypeRestaurant}
	sn := SpaceNote{SpaceID: s.ID, NoteID: n.ID, AddedBy: "a"}
	authorization, err := n.AuthorizeAuthorCollection("a", s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !n.CanCollectIn("a", s, members, authorization) || !sn.CanReadIn(n, "b", s, members) || !sn.CanRemoveIn("b", s, members) {
		t.Fatal("author grant or partner access missing")
	}
	if n.CanCollectIn("b", s, members, authorization) || n.CanEdit("b") || sn.CanReadIn(n, "outsider", s, members) {
		t.Fatal("read grant expanded ownership or membership")
	}
	private := space.Space{ID: "private", Type: space.TypePersonal, Name: "Mine", CreatorID: "b"}
	if n.CanCollectIn("b", private, []space.Member{{SpaceID: private.ID, UserID: "b", Role: space.RoleOwner}}, authorization) {
		t.Fatal("reader redistributed note into personal space")
	}
	sn.AddedBy = "b"
	if sn.CanReadIn(n, "b", s, members) {
		t.Fatal("unauthorized collection became a grant")
	}
	sn.AddedBy = "a"
	if sn.CanReadIn(n, "b", s, members[:1]) {
		t.Fatal("departed member retained access")
	}
}
