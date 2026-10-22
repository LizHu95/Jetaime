package ai

// RecognitionInput contains only the authorized, ordered conversation window.
type RecognitionInput struct {
	TriggerMode      string    `json:"triggerMode"`
	CurrentMessageID string    `json:"currentMessageId"`
	Messages         []Message `json:"messages"`
}

type Breakdown struct {
	Type               string   `json:"type"`
	EvidenceMessageIDs []string `json:"evidenceMessageIds"`
}

type InterventionIntent struct {
	Type               string   `json:"type"`
	Objective          string   `json:"objective"`
	EvidenceMessageIDs []string `json:"evidenceMessageIds"`
}

type Recognition struct {
	ShouldIntervene bool                `json:"shouldIntervene"`
	Reason          string              `json:"reason"`
	Breakdowns      []Breakdown         `json:"breakdowns"`
	Intent          *InterventionIntent `json:"intent"`
}
