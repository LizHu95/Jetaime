package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strings"
	"time"

	"github.com/alexedwards/argon2id"
)

func tokenDigest(token string) ([]byte, error) {
	raw, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(raw) != 32 || base64.RawURLEncoding.EncodeToString(raw) != token {
		return nil, ErrUnauthorized
	}
	hash := sha256.Sum256([]byte(token))
	return hash[:], nil
}

func (s *Service) Authenticate(ctx context.Context, token string) (User, error) {
	hash, err := tokenDigest(token)
	if err != nil {
		return User{}, err
	}
	return s.store.SessionUser(ctx, hash)
}

func (s *Service) ChangePassword(ctx context.Context, user User, oldPassword, newPassword string) error {
	if !validPassword(oldPassword, 1) || !validPassword(newPassword, 6) || oldPassword == newPassword {
		return ErrInvalidInput
	}
	if err := s.beginPasswordWork(ctx); err != nil {
		return err
	}
	defer func() { <-s.passwordWork }()
	_, oldHash, err := s.store.Credentials(ctx, user.Username)
	if err != nil {
		return err
	}
	match, err := argon2id.ComparePasswordAndHash(oldPassword, oldHash)
	if err != nil {
		return err
	}
	if !match {
		return ErrCredentials
	}
	hash, err := argon2id.CreateHash(newPassword, s.params)
	if err != nil {
		return err
	}
	return s.store.UpdatePassword(ctx, user.ID, oldHash, hash)
}

// RequireUser resolves the actor from a live database session on every request.
func (h *Handler) RequireUser(next func(http.ResponseWriter, *http.Request, User)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		values := r.Header.Values("Authorization")
		if len(values) != 1 {
			respondError(w, ErrUnauthorized)
			return
		}
		parts := strings.Fields(values[0])
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			respondError(w, ErrUnauthorized)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		user, err := h.service.Authenticate(ctx, parts[1])
		if err != nil {
			respondError(w, err)
			return
		}
		next(w, r.WithContext(ctx), user)
	}
}

func (h *Handler) changePassword(w http.ResponseWriter, r *http.Request, user User) {
	var input struct {
		OldPassword string `json:"oldPassword"`
		NewPassword string `json:"newPassword"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	if err := h.service.ChangePassword(r.Context(), user, input.OldPassword, input.NewPassword); err != nil {
		respondError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}
