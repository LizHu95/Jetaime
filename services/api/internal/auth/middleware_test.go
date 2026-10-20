package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"
)

type sessionStore struct {
	Store
	user User
	hash []byte
	err  error
}

func (s *sessionStore) SessionUser(_ context.Context, hash []byte) (User, error) {
	s.hash = hash
	return s.user, s.err
}

func TestAuthenticateIdentityAndRevocation(t *testing.T) {
	store := &sessionStore{user: User{ID: "trusted-user", Username: "member"}}
	handler := NewHandler(&Service{store: store})
	token := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	request := httptest.NewRequest("GET", "/", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("User-ID", "forged-user")
	if user := UserFromContext(request.Context()); user != (User{}) {
		t.Fatal("unauthenticated context has an identity")
	}
	calls := 0
	var authenticatedContext context.Context
	endpoint := handler.Authenticate(func(w http.ResponseWriter, r *http.Request) {
		calls++
		authenticatedContext = r.Context()
		if user := UserFromContext(r.Context()); user != store.user {
			t.Fatalf("session identity lost: %+v", user)
		}
		if _, ok := r.Context().Deadline(); !ok {
			t.Fatal("authentication timeout lost")
		}
		w.WriteHeader(http.StatusNoContent)
	})
	out := httptest.NewRecorder()
	endpoint.ServeHTTP(out, request)
	expectedHash := sha256.Sum256([]byte(token))
	if out.Code != http.StatusNoContent || calls != 1 || !bytes.Equal(store.hash, expectedHash[:]) {
		t.Fatalf("authenticated dispatch failed: status=%d calls=%d", out.Code, calls)
	}
	if authenticatedContext.Err() != context.Canceled {
		t.Fatal("request timeout context was not released")
	}
	if user := UserFromContext(request.Context()); user != (User{}) {
		t.Fatal("authenticated identity leaked into the original request")
	}
	store.err = ErrUnauthorized
	out = httptest.NewRecorder()
	endpoint.ServeHTTP(out, request)
	if out.Code != http.StatusUnauthorized || calls != 1 {
		t.Fatal("revoked session reused an earlier identity")
	}
}

func TestAuthenticateRejectsInvalidAuthorization(t *testing.T) {
	handler := NewHandler(nil)
	endpoint := handler.Authenticate(func(http.ResponseWriter, *http.Request) {
		t.Fatal("unauthenticated request reached the handler")
	})
	for _, values := range [][]string{nil, {"Basic abc"}, {"Bearer malformed"}, {"Bearer one", "Bearer two"}} {
		request := httptest.NewRequest("GET", "/", nil)
		for _, value := range values {
			request.Header.Add("Authorization", value)
		}
		// Malformed Bearer tokens need a Service to reject their format, but no store.
		handler.service = &Service{}
		out := httptest.NewRecorder()
		endpoint.ServeHTTP(out, request)
		if out.Code != http.StatusUnauthorized || out.Header().Get("WWW-Authenticate") != "Bearer" {
			t.Fatalf("invalid authorization accepted: status=%d", out.Code)
		}
	}
}
