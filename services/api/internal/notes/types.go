// Package notes defines note content and its collection relationships.
package notes

// Type is the V1 content category, independent of a user's preferences.
type Type string

const (
	TypeRestaurant Type = "restaurant"
	TypeRecipe     Type = "recipe"
	TypeActivity   Type = "activity"
	TypeMovie      Type = "movie"
	TypeOther      Type = "other"
)

// Note belongs to its creator; spaces collect it through SpaceNote.
// Absent attributes mean unknown, rather than zero or an inferred fact.
type Note struct {
	ID          string         `json:"id"`
	CreatorID   string         `json:"creatorId"`
	Title       string         `json:"title"`
	Description string         `json:"description"`
	Type        Type           `json:"type"`
	Tags        []string       `json:"tags"`
	SourceURL   string         `json:"sourceUrl,omitempty"`
	Attributes  map[string]any `json:"attributes,omitempty"`
}

// SpaceNote grants collection membership, not ownership of the note body.
type SpaceNote struct {
	SpaceID string `json:"spaceId"`
	NoteID  string `json:"noteId"`
	AddedBy string `json:"addedBy"`
}
