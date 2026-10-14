package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRejectedRequests(t *testing.T) {
	service, err := NewService(nil)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	NewHandler(service).RegisterRoutes(mux)
	for _, tc := range []struct {
		name, path, contentType, body, authorization string
		status                                       int
	}{
		{"content type", "/api/auth/register", "text/plain", `{}`, "", 415},
		{"malformed", "/api/auth/login", "application/json", `{"password":"private-value",`, "", 400},
		{"unknown fields", "/api/auth/register", "application/json", `{"role":"admin"}`, "", 400},
		{"multiple objects", "/api/auth/login", "application/json", `{} {}`, "", 400},
		{"oversized", "/api/auth/register", "application/json", `{"username":"` + strings.Repeat("a", 8192) + `"}`, "", 413},
		{"empty input", "/api/auth/register", "application/json", `{}`, "", 400},
		{"missing token", "/api/auth/logout", "", "", "", 401},
		{"malformed token", "/api/auth/logout", "", "", "Bearer malformed", 401},
		{"wrong scheme", "/api/auth/logout", "", "", "Basic dGVzdA==", 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", tc.contentType)
			req.Header.Set("Authorization", tc.authorization)
			out := httptest.NewRecorder()
			mux.ServeHTTP(out, req)
			if out.Code != tc.status || strings.Contains(out.Body.String(), "private-value") {
				t.Fatalf("status %d, body %s", out.Code, out.Body.String())
			}
			if out.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("missing no-store")
			}
		})
	}
	for _, path := range []string{"/api/auth/login", "/api/auth/logout", "/api/auth/register"} {
		out := httptest.NewRecorder()
		mux.ServeHTTP(out, httptest.NewRequest("GET", path, nil))
		if out.Code != 405 {
			t.Fatalf("GET %s: %d", path, out.Code)
		}
	}
}
