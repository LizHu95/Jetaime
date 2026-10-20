// Package chat provides durable messages, read cursors and shared AI replies.
package chat

import (
	"errors"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/auth"
	"github.com/LizHu95/Jetaime/services/api/internal/notification"
)

var (
	ErrAIUnavailable = errors.New("AI unavailable")
	ErrInvalid       = errors.New("invalid input")
	ErrNotFound      = ai.ErrNotFound
	ErrConflict      = ai.ErrConflict
	ErrContext       = ai.ErrContext
)

type Message struct {
	AgentKind   string     `json:"agentKind"`
	AgentResult *ai.Result `json:"agentResult,omitempty"`
	ID          string     `json:"id"`
	GroupID     string     `json:"groupId"`
	Seq         int64      `json:"seq"`
	SenderType  string     `json:"senderType"`
	SenderID    *string    `json:"senderId"`
	SenderName  string     `json:"senderName"`
	Content     string     `json:"content"`
	MentionsAI  bool       `json:"mentionsAI"`
	ClientID    *string    `json:"clientMessageId"`
	ReplyToID   *string    `json:"replyToId"`
	CreatedAt   time.Time  `json:"createdAt"`
}
type SendInput struct {
	AgentKind  string `json:"agentKind,omitempty"`
	Content    string `json:"content"`
	ClientID   string `json:"clientMessageId"`
	MentionsAI bool   `json:"mentionsAI"`
}
type State struct {
	ProactiveEnabled bool        `json:"proactiveEnabled"`
	ID               string      `json:"id"`
	Members          []auth.User `json:"members"`
	LastSeq          int64       `json:"lastSeq"`
	ReadSeq          int64       `json:"readSeq"`
	Unread           int64       `json:"unreadCount"`
	AssistantName    string      `json:"assistantName"`
	AIEnabled        bool        `json:"aiEnabled"`
}
type Page struct {
	Messages []Message `json:"messages"`
	HasMore  bool      `json:"hasMore"`
}
type Run = ai.Job
type Event = notification.Event
