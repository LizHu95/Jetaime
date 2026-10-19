// Package contacts implements consent-based two-person groups.
package collaboration

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/auth"
	"github.com/google/uuid"
)

var (
	ErrInvalid      = errors.New("invalid input")
	ErrNotFound     = errors.New("not found")
	ErrForbidden    = errors.New("forbidden")
	ErrConflict     = errors.New("request state conflict")
	ErrConnected    = errors.New("already connected")
	usernamePattern = regexp.MustCompile(`^[A-Za-z0-9_]{2,24}$`)
)

type Request struct {
	ID        string    `json:"id"`
	From      auth.User `json:"from"`
	To        auth.User `json:"to"`
	Status    string    `json:"status"`
	GroupID   *string   `json:"groupId"`
	CreatedAt time.Time `json:"createdAt"`
}
type Group struct {
	ID             string    `json:"id"`
	Peer           auth.User `json:"peer"`
	Status         string    `json:"status"`
	AssistantID    string    `json:"assistantId"`
	AssistantName  string    `json:"assistantName"`
	CreatedAt      time.Time `json:"createdAt"`
	UnreadCount    int64     `json:"unreadCount"`
	LastReadSeq    int64     `json:"lastReadSeq"`
	LastMessageSeq int64     `json:"lastMessageSeq"`
	LastMessage    string    `json:"lastMessage"`
}
type Store interface {
	FindUser(context.Context, string) (auth.User, error)
	Send(context.Context, string, string) (Request, error)
	Requests(context.Context, string) ([]Request, error)
	Resolve(context.Context, string, string, string) (Request, error)
	Groups(context.Context, string) ([]Group, error)
}
type Service struct{ store Store }

func NewService(store Store) *Service { return &Service{store: store} }
func (s *Service) FindUser(ctx context.Context, username string) (auth.User, error) {
	username = strings.TrimSpace(username)
	if !usernamePattern.MatchString(username) {
		return auth.User{}, ErrInvalid
	}
	return s.store.FindUser(ctx, username)
}
func (s *Service) Send(ctx context.Context, actor, username string) (Request, error) {
	user, err := s.FindUser(ctx, username)
	if err != nil {
		return Request{}, err
	}
	if user.ID == actor {
		return Request{}, ErrInvalid
	}
	return s.store.Send(ctx, actor, user.ID)
}
func (s *Service) Resolve(ctx context.Context, actor, id, action string) (Request, error) {
	if _, err := uuid.Parse(id); err != nil {
		return Request{}, ErrInvalid
	}
	if action != "accept" && action != "reject" && action != "cancel" {
		return Request{}, ErrInvalid
	}
	return s.store.Resolve(ctx, actor, id, action)
}
