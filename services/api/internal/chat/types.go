// Package chat provides durable messages, read cursors and shared AI replies.
package chat

import (
	"errors"
	"github.com/LizHu95/Jetaime/services/api/internal/assistant"
	"github.com/LizHu95/Jetaime/services/api/internal/auth"
	"time"
)

var (
	ErrInvalid  = errors.New("invalid input")
	ErrNotFound = errors.New("group or message not found")
	ErrConflict = errors.New("message or run conflict")
	ErrContext  = errors.New("context too large")
)

type Message struct {
	AgentKind   string            `json:"agentKind"`
	AgentResult *assistant.Result `json:"agentResult,omitempty"`
	ID          string            `json:"id"`
	GroupID     string            `json:"groupId"`
	Seq         int64             `json:"seq"`
	SenderType  string            `json:"senderType"`
	SenderID    *string           `json:"senderId"`
	SenderName  string            `json:"senderName"`
	Content     string            `json:"content"`
	MentionsAI  bool              `json:"mentionsAI"`
	ClientID    *string           `json:"clientMessageId"`
	ReplyToID   *string           `json:"replyToId"`
	CreatedAt   time.Time         `json:"createdAt"`
}
type SendInput struct {
	AgentKind  string `json:"agentKind,omitempty"`
	Content    string `json:"content"`
	ClientID   string `json:"clientMessageId"`
	MentionsAI bool   `json:"mentionsAI"`
}
type State struct {
	ID            string      `json:"id"`
	Members       []auth.User `json:"members"`
	LastSeq       int64       `json:"lastSeq"`
	ReadSeq       int64       `json:"readSeq"`
	Unread        int64       `json:"unreadCount"`
	AssistantName string      `json:"assistantName"`
	AIEnabled     bool        `json:"aiEnabled"`
}
type Page struct {
	Messages []Message `json:"messages"`
	HasMore  bool      `json:"hasMore"`
}
type Run struct {
	Kind        string  `json:"agentKind"`
	ID          string  `json:"id"`
	GroupID     string  `json:"groupId"`
	AssistantID string  `json:"-"`
	TriggerID   string  `json:"triggerMessageId"`
	RequestedBy string  `json:"requestedBy"`
	ContextEnd  int64   `json:"contextEndSeq"`
	Status      string  `json:"status"`
	Attempt     int     `json:"attempt"`
	ErrorCode   *string `json:"errorCode"`
	ResponseID  *string `json:"responseMessageId"`
}
type Event struct {
	Type    string `json:"type"`
	GroupID string `json:"groupId,omitempty"`
	ActorID string `json:"actorId,omitempty"`
}
