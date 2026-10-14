package webchat

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAccountSessionAndSharedRoom(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := NewServer(path, "test-invitation-code", "https://chat.example.com")
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path string, body any, cookie *http.Cookie) *httptest.ResponseRecorder {
		data, _ := json.Marshal(body)
		r := httptest.NewRequest(method, "https://chat.example.com"+path, bytes.NewReader(data))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "https://chat.example.com")
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	if w := request("GET", "/api/webchat/messages", nil, nil); w.Code != 401 {
		t.Fatalf("anonymous read: %d", w.Code)
	}
	if w := request("POST", "/api/webchat/register", map[string]string{"name": "Alice", "password": "strong-password", "invite": "wrong"}, nil); w.Code != 403 {
		t.Fatalf("invalid invitation: %d", w.Code)
	}
	w := request("POST", "/api/webchat/register", map[string]string{"name": "Alice", "password": "strong-password", "invite": "test-invitation-code"}, nil)
	if w.Code != 200 {
		t.Fatalf("registration: %d %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), "password") {
		t.Fatal("password hash leaked to client")
	}
	cookie := w.Result().Cookies()[0]
	if !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatal("unsafe session cookie")
	}
	if w = request("POST", "/api/webchat/messages", map[string]string{"text": "Hello", "userId": "someone-else"}, cookie); w.Code != 400 {
		t.Fatalf("identity injection accepted: %d", w.Code)
	}
	if w = request("POST", "/api/webchat/messages", map[string]string{"text": "Hello"}, cookie); w.Code != 200 {
		t.Fatalf("send: %d %s", w.Code, w.Body)
	}
	w = request("POST", "/api/webchat/register", map[string]string{"name": "Bob", "password": "another-password", "invite": "test-invitation-code"}, nil)
	if w.Code != 200 {
		t.Fatalf("second account: %d %s", w.Code, w.Body)
	}
	bobCookie := w.Result().Cookies()[0]
	w = request("GET", "/api/webchat/messages", nil, bobCookie)
	var room struct {
		Messages []Message `json:"messages"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &room); err != nil {
		t.Fatal(err)
	}
	if len(room.Messages) != 2 || room.Messages[0].Name != "Alice" || room.Messages[1].Text != "我一直都在" || !room.Messages[1].Bot {
		t.Fatalf("shared room: %+v", room)
	}
	if w = request("POST", "/api/webchat/logout", map[string]string{}, cookie); w.Code != 200 {
		t.Fatal("logout failed")
	}
	if w = request("GET", "/api/webchat/messages", nil, cookie); w.Code != 401 {
		t.Fatal("logout did not revoke session")
	}
	if w = request("POST", "/api/webchat/login", map[string]string{"name": "Alice", "password": "wrong-password"}, nil); w.Code != 401 {
		t.Fatal("wrong password accepted")
	}
	if w = request("POST", "/api/webchat/login", map[string]string{"name": "Alice", "password": "strong-password"}, nil); w.Code != 200 {
		t.Fatal("login failed")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("strong-password")) {
		t.Fatal("plaintext password persisted")
	}
	s2, err := NewServer(path, "test-invitation-code", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(s2.state.Users) != 2 || len(s2.state.Messages) != 2 {
		t.Fatal("data not persisted")
	}
	if len(s2.sessions) != 0 {
		t.Fatal("sessions should expire on restart")
	}
}

func TestCrossSiteRequestsAreRejected(t *testing.T) {
	s, err := NewServer(filepath.Join(t.TempDir(), "state.json"), "test-invitation-code", "https://chat.example.com")
	if err != nil {
		t.Fatal(err)
	}
	for _, origin := range []string{"https://evil.example.com", "null"} {
		r := httptest.NewRequest("POST", "https://chat.example.com/api/webchat/login", strings.NewReader(`{}`))
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("origin %s accepted", origin)
		}
	}
	r := httptest.NewRequest("GET", "https://evil.example.com/api/webchat/session", nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("untrusted host accepted")
	}
}
