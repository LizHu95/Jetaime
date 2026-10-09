// Package decisions defines the shared CLI and HTTP decision workflow contract.
package decisions

import (
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/memory"
	"github.com/LizHu95/Jetaime/services/api/internal/notes"
)

// Task describes the decision operation, independently of candidate note types.
// Planning tasks reserve contracts for later workflows; the initial runner must
// accept only TaskSelect and reject unsupported tasks before invoking a model.
type Task string

const (
	TaskSelect   Task = "select"
	TaskDayPlan  Task = "day_plan"
	TaskTripPlan Task = "trip_plan"
)

const MaxSelectRecommendations = 3

// Conditions are normalized, user-confirmed constraints for this request.
// Nil numeric limits mean unrestricted; zero is an explicit limit.
type Conditions struct {
	NoteTypes          []notes.Type        `json:"noteTypes"`
	BudgetMaxCents     *int64              `json:"budgetMaxCents"`
	DurationMaxMinutes *int                `json:"durationMaxMinutes"`
	HardConstraints    []string            `json:"hardConstraints"`
	SoftPreferences    []string            `json:"softPreferences"`
	Planning           *PlanningConditions `json:"planning,omitempty"`
}

// Request contains user intent, never client-supplied authoritative candidates.
// RequesterID must come from a trusted identity in HTTP; CLI uses mock identity.
type Request struct {
	SpaceID     string     `json:"spaceId"`
	RequesterID string     `json:"requesterId"`
	Task        Task       `json:"task"`
	Query       string     `json:"query"`
	Conditions  Conditions `json:"conditions"`
	SessionID   string     `json:"sessionId,omitempty"`
}

// Participant is assembled from authorized space members and their memories.
// All applicable hard constraints must be loaded independently of vector Top K.
type Participant struct {
	UserID           string          `json:"userId"`
	HardConstraints  []memory.Memory `json:"hardConstraints"`
	RelevantMemories []memory.Memory `json:"relevantMemories"`
}

// Context is server-assembled input to a model, not a client request or DB row.
// Candidates contain the note facts needed to explain and validate the result.
type Context struct {
	Task         Task          `json:"task"`
	Query        string        `json:"query"`
	Conditions   Conditions    `json:"conditions"`
	Participants []Participant `json:"participants"`
	Candidates   []notes.Note  `json:"candidates"`
}

type ParticipantMatch struct {
	UserID      string `json:"userId"`
	Explanation string `json:"explanation"`
}

// Option is one alternative within a decision. The service assigns its
// OptionID after decoding model output, before final Result validation; it must
// be unique within the decision.
// Exactly one payload is present: Selection for select, Itinerary for planning.
// These are alternatives, not three consecutive steps in a single plan.
type Option struct {
	OptionID           string             `json:"optionId"`
	Title              string             `json:"title"`
	Selection          *Selection         `json:"selection,omitempty"`
	Itinerary          *Itinerary         `json:"itinerary,omitempty"`
	Reason             string             `json:"reason"`
	ParticipantMatches []ParticipantMatch `json:"participantMatches"`
	Unknowns           []string           `json:"unknowns"`
}

// Selection is the initial task's payload: one authorized candidate note.
type Selection struct {
	NoteID string `json:"noteId"`
}

type Outcome string

const (
	OutcomeRecommended        Outcome = "recommended"
	OutcomeNoCandidates       Outcome = "no_candidates"
	OutcomeConstraintConflict Outcome = "constraint_conflict"
	OutcomeInsufficientInfo   Outcome = "insufficient_information"
)

// Result holds alternatives for the requested task. Select returns up to three;
// planning item counts and alternative limits belong to their future workflows.
// Runtime validation must enforce payload exclusivity, IDs and hard constraints.
// Provider/transport failures return errors, rather than fabricated Results.
type Result struct {
	Outcome     Outcome  `json:"outcome"`
	Options     []Option `json:"options"`
	Explanation string   `json:"explanation"`
}

type FeedbackAction string

const (
	FeedbackAdopt       FeedbackAction = "adopt"
	FeedbackReject      FeedbackAction = "reject"
	FeedbackChangeBatch FeedbackAction = "change_batch"
)

// Feedback is an event attached to a decision; it does not mutate Memory.
// OptionID must reference an option in DecisionID for adopt/reject and must be
// absent for change_batch. Rejecting an option does not reject its Note forever.
type Feedback struct {
	ID         string         `json:"id"`
	DecisionID string         `json:"decisionId"`
	UserID     string         `json:"userId"`
	Action     FeedbackAction `json:"action"`
	OptionID   string         `json:"optionId,omitempty"`
	Reason     string         `json:"reason,omitempty"`
	CreatedAt  time.Time      `json:"createdAt"`
}

// Decision is a history snapshot independent of model Context and current Notes.
// It does not retain all private memories or the entire model prompt by default.
type Decision struct {
	ID             string       `json:"id"`
	SessionID      string       `json:"sessionId"`
	SpaceID        string       `json:"spaceId"`
	RequesterID    string       `json:"requesterId"`
	Task           Task         `json:"task"`
	ParticipantIDs []string     `json:"participantIds"`
	Query          string       `json:"query"`
	Conditions     Conditions   `json:"conditions"`
	CandidateNotes []notes.Note `json:"candidateNotes"`
	Result         Result       `json:"result"`
	Feedback       []Feedback   `json:"feedback"`
	CreatedAt      time.Time    `json:"createdAt"`
}

// Session manages temporary refinement in one authenticated user/space/task scope.
// Each generated batch has its own Decision; a session is not permanent memory.
type Session struct {
	ID                 string     `json:"id"`
	SpaceID            string     `json:"spaceId"`
	RequesterID        string     `json:"requesterId"`
	Task               Task       `json:"task"`
	Query              string     `json:"query"`
	Conditions         Conditions `json:"conditions"`
	RecommendedNoteIDs []string   `json:"recommendedNoteIds"`
	ExcludedNoteIDs    []string   `json:"excludedNoteIds"`
	LatestDecisionID   string     `json:"latestDecisionId,omitempty"`
	AdoptedOption      *OptionRef `json:"adoptedOption,omitempty"`
	ExpiresAt          time.Time  `json:"expiresAt"`
}

// OptionRef identifies the current adoption across batches within a session.
// Decision.AdoptedOptionID remains a batch-local historical projection.
type OptionRef struct {
	DecisionID string `json:"decisionId"`
	OptionID   string `json:"optionId"`
	// Cached select note identity allows an old-batch rejection of the same note
	// to clear the current adoption without loading another decision.
	NoteID string `json:"noteId"`
}
