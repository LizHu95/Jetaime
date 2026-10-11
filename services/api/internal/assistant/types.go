// Package assistant implements the local V2 history-QA and task-extraction workflows.
package assistant

import (
	"encoding/json"
	"time"
)

type User struct {
	ID       string `json:"id"`
	Nickname string `json:"nickname"`
}
type Group struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type GroupMember struct {
	GroupID string `json:"groupId"`
	UserID  string `json:"userId"`
}
type Message struct {
	ID       string    `json:"id"`
	GroupID  string    `json:"groupId"`
	SenderID string    `json:"senderId"`
	SentAt   time.Time `json:"sentAt"`
	Text     string    `json:"text"`
}
type Fixture struct {
	Users    []User        `json:"users"`
	Groups   []Group       `json:"groups"`
	Members  []GroupMember `json:"members"`
	Messages []Message     `json:"messages"`
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
type diskState struct {
	Messages []Message `json:"messages"`
	Tasks    []Task    `json:"tasks"`
}
type Context struct {
	ActorID  string    `json:"actorId"`
	GroupID  string    `json:"groupId"`
	Users    []User    `json:"users"`
	Messages []Message `json:"messages"`
	Tasks    []Task    `json:"tasks"`
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
	ID            string       `json:"id"`
	GroupID       string       `json:"groupId"`
	ActorID       string       `json:"actorId"`
	Kind          string       `json:"kind"`
	Question      string       `json:"question,omitempty"`
	Model         string       `json:"model"`
	PromptVersion string       `json:"promptVersion"`
	StartedAt     time.Time    `json:"startedAt"`
	DurationMS    int64        `json:"durationMs"`
	Status        string       `json:"status"`
	Steps         []Step       `json:"steps"`
	Answer        *Answer      `json:"answer,omitempty"`
	Suggestions   []Suggestion `json:"suggestions,omitempty"`
	Sources       []Message    `json:"sources,omitempty"`
	Error         string       `json:"error,omitempty"`
	Warning       string       `json:"warning,omitempty"`
}
type ModelRequest struct {
	Model    string          `json:"model"`
	Messages []ChatMessage   `json:"messages"`
	Format   json.RawMessage `json:"format"`
	Stream   bool            `json:"stream"`
	Think    bool            `json:"think"`
	Options  ModelOptions    `json:"options"`
}
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type ModelOptions struct {
	Temperature float64 `json:"temperature"`
	NumCtx      int     `json:"num_ctx"`
	NumPredict  int     `json:"num_predict"`
}
type ModelResult struct {
	Content          string
	Raw              any
	PromptTokens     *int
	CompletionTokens *int
}
