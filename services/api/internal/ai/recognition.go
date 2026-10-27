package ai

import "context"

// InterventionMessageLimit is the shared passive-recognition window size.
const InterventionMessageLimit = 30

// Recognizer judges conversation evidence without executing the main agent.
type Recognizer interface {
	Discover(context.Context, RecognitionInput) (CandidateResult, error)
	Decide(context.Context, VerificationInput) (RecognitionResult, error)
}

// InterventionCheck is supplied by the caller after recognition and before
// unsolicited assistance. It returns a stable suppression reason, or empty to
// proceed; persistence and authorization remain in the chat domain.
type InterventionCheck func(context.Context, string, Recognition) (string, error)

// SuppressIntervention preserves a successful, silent policy decision.
func SuppressIntervention(decision Recognition, reason string) Recognition {
	decision.ShouldIntervene = false
	decision.Intent = nil
	decision.Breakdowns = []Breakdown{}
	decision.Reason = "主动介入已抑制：" + reason
	return decision
}

type RecognitionResult struct {
	Decision      Recognition
	Steps         []Step
	Model         string
	PromptVersion string
}

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

// InterventionCandidate describes a bounded next step, not a tool call.
// TopicContext separates the current discussion from nearby unrelated history.
// Anchors name the original messages that define the subject and must survive trimming.
type TopicContext struct {
	Topic            string   `json:"topic"`
	Continuity       string   `json:"continuity"`
	MessageIDs       []string `json:"messageIds"`
	AnchorMessageIDs []string `json:"anchorMessageIds"`
}

type InterventionCandidate struct {
	TopicContext *TopicContext         `json:"topicContext"`
	Action       string                `json:"action"`
	Reason       string                `json:"reason"`
	Breakdowns   []Breakdown           `json:"breakdowns"`
	Intent       *InterventionIntent   `json:"intent"`
	Verification *VerificationQuestion `json:"verification"`
}

type VerificationQuestion struct {
	Topic              string   `json:"topic"`
	Query              string   `json:"query"`
	EvidenceMessageIDs []string `json:"evidenceMessageIds"`
}

type CandidateResult struct {
	Decision      InterventionCandidate
	Steps         []Step
	Model         string
	PromptVersion string
}

type VerificationInput struct {
	Conversation RecognitionInput      `json:"conversation"`
	Candidate    InterventionCandidate `json:"candidate"`
	Evidence     InterventionEvidence  `json:"evidence"`
}

// These provider arguments are server-owned and never parsed from model output.
type InterventionEvidenceRequest struct {
	Context       Context
	ContextEndSeq int64
	Question      VerificationQuestion
}

type InterventionEvidence struct {
	Messages  []Message `json:"messages"`
	Coverage  string    `json:"coverage"` // partial: sparse retrieval cannot establish absence of revisions.
	Truncated bool      `json:"truncated"`
}

// InterventionEvidenceProvider retrieves original messages for historical verification.
type InterventionEvidenceProvider interface {
	Retrieve(context.Context, InterventionEvidenceRequest) (InterventionEvidence, error)
}

const MaxInterventionEvidenceMessages = 50
