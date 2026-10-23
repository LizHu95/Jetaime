// Package history defines original chat messages and bounded read capabilities.
package history

import (
	"context"
	"time"
)

type Message struct {
	SenderType string    `json:"senderType,omitempty"`
	ID         string    `json:"id"`
	Seq        int64     `json:"seq"`
	SenderID   string    `json:"senderId"`
	SenderName string    `json:"senderName"`
	Text       string    `json:"text"`
	SentAt     time.Time `json:"sentAt"`
}

type Reader interface {
	RecentMessages(context.Context, string, string, int64, int) ([]Message, error)
	SourceMessages(context.Context, string, string, int64, []string) ([]Message, error)
}
