package ai

import (
	"context"
	"errors"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
)

var (
	ErrInvalidDecision = errors.New("invalid main agent decision")
	ErrNotFound        = errors.New("group or message not found")
	ErrConflict        = errors.New("message or run conflict")
	ErrContext         = llm.ErrContextLimit
)

type ExtractContextLoader func(context.Context) (Context, error)

// Invocation separates the registered agent from its requested capability.
// Mode retains compatibility with explicit qa/help requests.
type Invocation struct {
	TriggerMode    string
	ContextEndSeq  int64
	AgentID        string
	Context        Context
	Mode           string
	Question       string
	LoadExtraction ExtractContextLoader
}

type Agent interface {
	ID() string
	Invoke(context.Context, Invocation) (Run, error)
}

type Executor interface {
	Execute(context.Context, Invocation) (Run, error)
}

// Job is durable scheduling state, independent from an agent's execution Run.
type Job struct {
	TriggerMentionsAI bool         `json:"-"`
	TriggerMode       string       `json:"triggerMode"`
	Recognition       *Recognition `json:"recognition,omitempty"`
	TraceID           *string      `json:"traceId"`
	TraceParent       string       `json:"-"`
	TraceState        string       `json:"-"`
	QueuedAt          time.Time    `json:"-"`
	Kind              string       `json:"agentKind"`
	ID                string       `json:"id"`
	GroupID           string       `json:"groupId"`
	AssistantID       string       `json:"-"`
	TriggerID         string       `json:"triggerMessageId"`
	RequestedBy       string       `json:"requestedBy"`
	ContextEnd        int64        `json:"contextEndSeq"`
	Status            string       `json:"status"`
	Attempt           int          `json:"attempt"`
	ErrorCode         *string      `json:"errorCode"`
	ResponseID        *string      `json:"responseMessageId"`
}
