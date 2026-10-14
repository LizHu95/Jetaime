// Package auth implements account credentials and database-backed login sessions.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/alexedwards/argon2id"
)

var (
	ErrInvalidInput  = errors.New("invalid input")
	ErrUsernameTaken = errors.New("username already registered")
	ErrCredentials   = errors.New("invalid username or password")
	ErrUnauthorized  = errors.New("invalid or expired session")
	ErrBusy          = errors.New("authentication busy; try again later")
	errNotFound      = errors.New("credentials not found")
	usernamePattern  = regexp.MustCompile(`^[A-Za-z0-9_]{2,24}$`)
)

type User struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Nickname string `json:"nickname"`
}

type LoginResult struct {
	User      User      `json:"user"`
	Token     string    `json:"token"`
	TokenType string    `json:"tokenType"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// Store keeps persistence details outside the service and HTTP handlers.
type Store interface {
	UsernameExists(context.Context, string) (bool, error)
	CreateUser(context.Context, string, string, string) (User, error)
	Credentials(context.Context, string) (User, string, error)
	CreateSession(context.Context, string, []byte, time.Duration) (time.Time, error)
	RevokeSession(context.Context, []byte) error
}

type Service struct {
	store        Store
	params       *argon2id.Params
	dummyHash    string
	passwordWork chan struct{}
}

func NewService(store Store) (*Service, error) {
	// Explicit parameters keep CPU and memory use independent of machine size.
	params := &argon2id.Params{Memory: 19 * 1024, Iterations: 2, Parallelism: 1, SaltLength: 16, KeyLength: 32}
	dummy, err := argon2id.CreateHash("dummy password for nonexistent accounts", params)
	if err != nil {
		return nil, err
	}
	return &Service{store: store, params: params, dummyHash: dummy, passwordWork: make(chan struct{}, 2)}, nil
}

func normalizeUsername(username string) (string, error) {
	username = strings.TrimSpace(username)
	if !usernamePattern.MatchString(username) {
		return "", ErrInvalidInput
	}
	return username, nil
}

func validPassword(password string, minimum int) bool {
	return utf8.ValidString(password) && utf8.RuneCountInString(password) >= minimum && utf8.RuneCountInString(password) <= 128
}

// beginPasswordWork bounds the number of expensive hashes; waiting requests
// cannot build an unbounded queue of password hashing jobs.
func (s *Service) beginPasswordWork(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case s.passwordWork <- struct{}{}:
		return nil
	default:
		return ErrBusy
	}
}

func (s *Service) Register(ctx context.Context, username, password, nickname string) (User, error) {
	username, err := normalizeUsername(username)
	if err != nil || !validPassword(password, 6) {
		return User{}, ErrInvalidInput
	}
	nickname = strings.TrimSpace(nickname)
	if nickname == "" {
		nickname = username
	}
	if !utf8.ValidString(nickname) || strings.ContainsRune(nickname, '\x00') || utf8.RuneCountInString(nickname) > 24 {
		return User{}, ErrInvalidInput
	}
	exists, err := s.store.UsernameExists(ctx, username)
	if err != nil {
		return User{}, err
	}
	if exists {
		return User{}, ErrUsernameTaken
	}
	// The database unique index still arbitrates simultaneous registrations
	// that both observe an available username before hashing their passwords.
	if err := s.beginPasswordWork(ctx); err != nil {
		return User{}, err
	}
	defer func() { <-s.passwordWork }()
	hash, err := argon2id.CreateHash(password, s.params)
	if err != nil {
		return User{}, err
	}
	return s.store.CreateUser(ctx, username, nickname, hash)
}

func (s *Service) Login(ctx context.Context, username, password string) (LoginResult, error) {
	username, err := normalizeUsername(username)
	if err != nil || !validPassword(password, 1) {
		return LoginResult{}, ErrCredentials
	}
	if err := s.beginPasswordWork(ctx); err != nil {
		return LoginResult{}, err
	}
	defer func() { <-s.passwordWork }()
	user, hash, err := s.store.Credentials(ctx, username)
	missing := errors.Is(err, errNotFound)
	if err != nil && !missing {
		return LoginResult{}, err
	}
	if missing {
		hash = s.dummyHash
	}
	// Unknown users incur the same password hashing work as incorrect passwords.
	match, err := argon2id.ComparePasswordAndHash(password, hash)
	if err != nil {
		return LoginResult{}, err
	}
	if missing || !match {
		return LoginResult{}, ErrCredentials
	}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return LoginResult{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(random)
	tokenHash := sha256.Sum256([]byte(token))
	expires, err := s.store.CreateSession(ctx, user.ID, tokenHash[:], 7*24*time.Hour)
	if err != nil {
		return LoginResult{}, err
	}
	return LoginResult{User: user, Token: token, TokenType: "Bearer", ExpiresAt: expires.UTC()}, nil
}

func (s *Service) Logout(ctx context.Context, token string) error {
	if len(token) != 43 {
		return ErrUnauthorized
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(raw) != 32 || base64.RawURLEncoding.EncodeToString(raw) != token {
		return ErrUnauthorized
	}
	hash := sha256.Sum256([]byte(token))
	return s.store.RevokeSession(ctx, hash[:])
}
