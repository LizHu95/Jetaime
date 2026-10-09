package space

import "testing"

func TestMultiSpaceAndMemberBoundaries(t *testing.T) {
	personal := Space{ID: "personal", CreatorID: "a", Name: "Private", Type: TypePersonal}
	couple := Space{ID: "couple", CreatorID: "a", Name: "Shared", Type: TypeCouple}
	p := []Member{{SpaceID: personal.ID, UserID: "a", Role: RoleOwner}}
	c := []Member{{SpaceID: couple.ID, UserID: "a", Role: RoleOwner}, {SpaceID: couple.ID, UserID: "b", Role: RoleMember}}
	if err := personal.ValidateMembers(p); err != nil {
		t.Fatal(err)
	}
	if err := couple.ValidateMembers(c); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		s    Space
		m    []Member
	}{
		{"personal guest", personal, append(p, Member{SpaceID: personal.ID, UserID: "b", Role: RoleMember})},
		{"third partner", couple, append(c, Member{SpaceID: couple.ID, UserID: "c", Role: RoleMember})},
		{"duplicate user", couple, []Member{c[0], c[0]}},
		{"foreign member", couple, p},
		{"missing owner", couple, []Member{c[1]}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.s.ValidateMembers(tc.m) == nil {
				t.Fatal("invalid membership accepted")
			}
		})
	}
}
