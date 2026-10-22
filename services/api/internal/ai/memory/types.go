// Package memory maintains durable, evidence-backed group history and memories.
// It is independent of the main Agent and has no business task write permissions.
package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"
)

var (
	ErrUnavailable = errors.New("memory models are not configured")
	ErrNotFound    = errors.New("group not found")
	ErrLease       = errors.New("memory lease lost")
	ErrInvalid     = errors.New("invalid memory input or model result")
	ErrPipeline    = errors.New("memory pipeline changed; explicit reindex required")
	ErrBusy        = errors.New("embedding task is leased or waiting for retry")
)

type Message struct {
	ID         string    `json:"id"`
	Seq        int64     `json:"seq"`
	SenderID   string    `json:"senderId"`
	SenderName string    `json:"senderName"`
	Text       string    `json:"text"`
	SentAt     time.Time `json:"sentAt"`
}
type Batch struct {
	GroupID    string      `json:"groupId"`
	From       int64       `json:"fromSeq"`
	To         int64       `json:"toSeq"`
	Messages   []Message   `json:"messages"`
	Members    []Person    `json:"members"`
	Token      string      `json:"-"`
	Candidates []Candidate `json:"-"`
	Extracted  bool        `json:"-"`
	Mutations  []Mutation  `json:"-"`
}
type Person struct {
	ID       string `json:"id"`
	Nickname string `json:"nickname"`
}
type Candidate struct {
	Kind           string   `json:"kind"`
	Subject        string   `json:"subject"`
	EventKey       string   `json:"eventKey"`
	TemporalScope  string   `json:"temporalScope"`
	Content        string   `json:"content"`
	SourceIDs      []string `json:"sourceMessageIds"`
	ExplicitChange bool     `json:"explicitChange"`
}
type Document struct {
	ID            string    `json:"id"`
	LogicalID     string    `json:"logicalId"`
	Revision      int       `json:"revision"`
	Kind          string    `json:"kind"`
	Subject       string    `json:"subject"`
	EventKey      string    `json:"eventKey"`
	TemporalScope string    `json:"temporalScope"`
	Content       string    `json:"content"`
	SourceIDs     []string  `json:"sourceMessageIds"`
	Start         time.Time `json:"startAt"`
	End           time.Time `json:"endAt"`
	Score         float64   `json:"score"`
}
type Decision struct {
	Action   string `json:"action"`
	TargetID string `json:"targetId"`
	Content  string `json:"content"`
}
type Extractor interface {
	Extract(context.Context, Batch) ([]Candidate, error)
	Reconcile(context.Context, Candidate, []Document) (Decision, error)
}
type Embedder interface {
	Embed(context.Context, string) ([]float32, error)
}

type Mutation struct {
	Candidate Candidate
	Decision  Decision
}

func hash(s string) string { v := sha256.Sum256([]byte(s)); return hex.EncodeToString(v[:]) }

const MaxBatchBytes = 18000
