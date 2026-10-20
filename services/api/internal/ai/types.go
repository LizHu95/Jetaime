// Package ai defines execution contracts shared by every entry point.
package ai

import (
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
)

type User struct {
	ID       string `json:"id"`
	Nickname string `json:"nickname"`
}
type Message struct {
	SenderType string    `json:"senderType,omitempty"`
	SenderName string    `json:"senderName,omitempty"`
	ReplyToID  *string   `json:"replyToId,omitempty"`
	ID         string    `json:"id"`
	GroupID    string    `json:"groupId"`
	SenderID   string    `json:"senderId"`
	SentAt     time.Time `json:"sentAt"`
	Text       string    `json:"text"`
}
type Task struct {
	ID               string     `json:"id"`
	GroupID          string     `json:"groupId"`
	Title            string     `json:"title"`
	AssigneeID       string     `json:"assigneeId"`
	DueAt            *time.Time `json:"dueAt"`
	Status           string     `json:"status"`
	SourceMessageIDs []string   `json:"sourceMessageIds"`
	ConfirmationKey  string     `json:"confirmationKey"`
}
type ReminderDraft struct {
	ID      string `json:"id"`
	Version int64  `json:"version"`
	Title   string `json:"title"`
	Date    string `json:"date"`
	Time    string `json:"time"`
}

type Context struct {
	ReminderDraft *ReminderDraft  `json:"reminderDraft,omitempty"`
	Request       *RequestContext `json:"request,omitempty"`
	ActorID       string          `json:"actorId"`
	GroupID       string          `json:"groupId"`
	Users         []User          `json:"users"`
	Messages      []Message       `json:"messages"`
	Tasks         []Task          `json:"tasks"`
	HistoryScope  *HistoryScope   `json:"historyScope,omitempty"`
}

// RequestContext is execution metadata, separate from untrusted chat content.
type RequestContext struct {
	TriggerMode       string    `json:"triggerMode,omitempty"`
	AllowWrites       bool      `json:"-"`
	JobID             string    `json:"-"`
	JobAttempt        int       `json:"-"`
	MessageID         string    `json:"messageId"`
	SenderID          string    `json:"senderId"`
	SentAt            time.Time `json:"sentAt"`
	CurrentTime       time.Time `json:"currentTime"`
	Timezone          string    `json:"timezone"`
	RelatedMessageIDs []string  `json:"relatedMessageIds,omitempty"`
}

// HistoryScope describes selection of original messages, not a summary of them.
type HistoryScope struct {
	RetrievedMessageIDs    []string `json:"-"` // Internal evidence priority, never a grant to cite missing messages.
	HistoricalSearch       bool     `json:"historicalSearch,omitempty"`
	Policy                 string   `json:"policy"`
	EarlierMessagesOmitted bool     `json:"earlierMessagesOmitted"`
}
type Answer struct {
	Status           string   `json:"status"`
	Answer           string   `json:"answer"`
	SourceMessageIDs []string `json:"sourceMessageIds"`
}
type Suggestion struct {
	ID                   string     `json:"id,omitempty"`
	Title                string     `json:"title"`
	AssigneeID           *string    `json:"assigneeId"`
	TimeExpression       string     `json:"timeExpression"`
	SourceMessageIDs     []string   `json:"sourceMessageIds"`
	DueAt                *time.Time `json:"dueAt,omitempty"`
	PossibleDuplicateIDs []string   `json:"possibleDuplicateIds,omitempty"`
}
type Extraction struct {
	Suggestions []Suggestion `json:"suggestions"`
}
type Step struct {
	StartedAt  time.Time `json:"startedAt"`
	DurationNS int64     `json:"durationNs"`
	Name       string    `json:"name"`
	Status     string    `json:"status"`
	DurationMS int64     `json:"durationMs"`
	Input      any       `json:"input,omitempty"`
	Output     any       `json:"output,omitempty"`
	Error      string    `json:"error,omitempty"`
}
type Run struct {
	Silent        bool           `json:"silent,omitempty"`
	Recognition   *Recognition   `json:"recognition,omitempty"`
	AgentID       string         `json:"agentId,omitempty"`
	InvocationID  string         `json:"invocationId,omitempty"`
	Basis         string         `json:"basis,omitempty"`
	Route         *RouteDecision `json:"route,omitempty"`
	HistoryScope  *HistoryScope  `json:"historyScope,omitempty"`
	ModelIdentity *llm.Identity  `json:"modelIdentity,omitempty"`
	ID            string         `json:"id"`
	GroupID       string         `json:"groupId"`
	ActorID       string         `json:"actorId"`
	Kind          string         `json:"kind"`
	Question      string         `json:"question,omitempty"`
	Model         string         `json:"model"`
	PromptVersion string         `json:"promptVersion"`
	StartedAt     time.Time      `json:"startedAt"`
	DurationMS    int64          `json:"durationMs"`
	Status        string         `json:"status"`
	Steps         []Step         `json:"steps"`
	Answer        *Answer        `json:"answer,omitempty"`
	Suggestions   []Suggestion   `json:"suggestions,omitempty"`
	Sources       []Message      `json:"sources,omitempty"`
	Error         string         `json:"error,omitempty"`
	Warning       string         `json:"warning,omitempty"`
}

type RouteDecision struct {
	Kind    string `json:"kind"`
	Version string `json:"version"`
}

type Result struct {
	Basis         string         `json:"basis,omitempty"`
	Route         *RouteDecision `json:"route,omitempty"`
	HistoryScope  *HistoryScope  `json:"historyScope,omitempty"`
	Kind          string         `json:"kind"`
	PromptVersion string         `json:"promptVersion"`
	Answer        *Answer        `json:"answer,omitempty"`
	Suggestions   []Suggestion   `json:"suggestions,omitempty"`
}
