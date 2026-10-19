package ai

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/llm"
)

var (
	ErrInvalidDecision = errors.New("invalid main agent decision")
	ErrNotFound        = errors.New("group or message not found")
	ErrConflict        = errors.New("message or run conflict")
	ErrContext         = llm.ErrContextLimit
)

type ExtractContextLoader func(context.Context) (Context, error)

// Invocation separates the registered agent from its requested capability.
// Mode retains compatibility with explicit qa/help/extract requests.
type Invocation struct {
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

// Service is the common entry point for production, console and evaluation.
// Assembly supplies the runtime; this package has no concrete agent dependency.
type Service struct{ executor Executor }

func NewService(executor Executor) (*Service, error) {
	if executor == nil {
		return nil, fmt.Errorf("AI executor is required")
	}
	return &Service{executor: executor}, nil
}

func (s *Service) Execute(ctx context.Context, in Invocation) (Run, error) {
	if in.AgentID == "" {
		in.AgentID = "main"
	}
	return s.executor.Execute(ctx, in)
}

// Job is durable scheduling state, independent from an agent's execution Run.
type Job struct {
	TraceParent string    `json:"-"`
	TraceState  string    `json:"-"`
	QueuedAt    time.Time `json:"-"`
	Kind        string    `json:"agentKind"`
	ID          string    `json:"id"`
	GroupID     string    `json:"groupId"`
	AssistantID string    `json:"-"`
	TriggerID   string    `json:"triggerMessageId"`
	RequestedBy string    `json:"requestedBy"`
	ContextEnd  int64     `json:"contextEndSeq"`
	Status      string    `json:"status"`
	Attempt     int       `json:"attempt"`
	ErrorCode   *string   `json:"errorCode"`
	ResponseID  *string   `json:"responseMessageId"`
}
