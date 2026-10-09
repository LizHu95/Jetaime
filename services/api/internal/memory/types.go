// Package memory defines user-owned preferences, constraints and feedback memory.
package memory

type Type string

const (
	TypePreference Type = "preference"
	TypeConstraint Type = "constraint"
	TypeFeedback   Type = "feedback"
)

type Strength string

const (
	StrengthLow    Strength = "low"
	StrengthMedium Strength = "medium"
	StrengthStrong Strength = "strong"
	StrengthHard   Strength = "hard"
)

type Source string

const (
	SourceExplicit Source = "explicit"
	SourceFeedback Source = "feedback"
	SourceInferred Source = "inferred"
)

// Memory is not a note attribute or an individual decision feedback event.
// Unconfirmed inferred memory must not become a permanent hard constraint.
type Memory struct {
	ID        string   `json:"id"`
	UserID    string   `json:"userId"`
	Type      Type     `json:"type"`
	Content   string   `json:"content"`
	Strength  Strength `json:"strength"`
	Source    Source   `json:"source"`
	Confirmed bool     `json:"confirmed"`
	// Empty means private. Shared consent includes use and relevant explanations
	// visible to current members of that space; it does not expose the memory list.
	AllowedSpaceIDs []string `json:"allowedSpaceIds"`
}
