// Package space defines personal and couple collection boundaries.
package space

type Type string

const (
	TypePersonal Type = "personal"
	TypeCouple   Type = "couple"
)

type Space struct {
	ID        string `json:"id"`
	Type      Type   `json:"type"`
	Name      string `json:"name"`
	CreatorID string `json:"creatorId"`
}

// Member is the authoritative membership input to authorization checks.
// Supplying a participant ID in a decision request does not grant membership.
type Member struct {
	SpaceID string `json:"spaceId"`
	UserID  string `json:"userId"`
	Role    Role   `json:"role"`
}

type Role string

const (
	RoleOwner  Role = "owner"
	RoleMember Role = "member"
)
