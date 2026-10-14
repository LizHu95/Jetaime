package httpapi

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LizHu95/Jetaime/services/api/internal/assistant"
	"github.com/LizHu95/Jetaime/services/api/internal/webchat"
)

func TestAssembledModules(t *testing.T) {
	chat, err := webchat.NewServer(filepath.Join(t.TempDir(), "webchat.json"), "test-invitation-code", "https://chat.example.com")
	if err != nil {
		t.Fatal(err)
	}
	local := assistant.NewServer(&assistant.Workflow{Store: &assistant.Store{}})
	router := NewRouter(Dependencies{Assistant: local, Webchat: chat})
	for _, tc := range []struct {
		method, url, origin, contains string
		status                        int
	}{
		{"GET", "http://127.0.0.1/api/health", "", `"status":"ok"`, 200},
		{"GET", "http://127.0.0.1/page/assistant/", "", "Workflow 调试台", 200},
		{"GET", "http://127.0.0.1/page/assistant/app.js", "", "/api/assistant/", 200},
		{"GET", "http://127.0.0.1/page/assistant/style.css", "", "", 200},
		{"GET", "http://127.0.0.1/api/assistant/catalog", "", "catalog", 200},
		{"POST", "http://127.0.0.1/api/assistant/catalog", "", "", 405},
		{"GET", "https://chat.example.com/api/assistant/catalog", "", "", 403},
		{"GET", "http://127.0.0.1/api/assistant/catalog", "https://evil.example", "", 403},
		{"GET", "https://chat.example.com/page/webchat/", "", "聊天室", 200},
		{"GET", "https://chat.example.com/page/webchat/app.js", "", "/api/webchat/", 200},
		{"GET", "https://chat.example.com/page/webchat/style.css", "", "", 200},
		{"GET", "https://chat.example.com/api/health", "", `"status":"ok"`, 200},
		{"POST", "https://chat.example.com/api/health", "", "", 405},
		{"GET", "https://chat.example.com/api/webchat/messages", "", "", 401},
		{"GET", "https://chat.example.com/api/webchat/session", "https://evil.example", "", 403},
		{"GET", "http://127.0.0.1/api/webchat/session", "", "", 403},
		{"GET", "http://127.0.0.1/api/assistant/missing", "", "", 404},
		{"GET", "https://chat.example.com/api/webchat/missing", "", "", 404},
		{"GET", "http://127.0.0.1/api/messages", "", "", 404},
		{"GET", "http://127.0.0.1/api/test/run", "", "", 404},
		{"GET", "http://127.0.0.1/style.css", "", "", 404},
		{"GET", "http://127.0.0.1/assistant/", "", "", 404},
		{"GET", "https://chat.example.com/webchat/", "", "", 404},
		{"GET", "http://127.0.0.1/healthz", "", "", 404},
		{"GET", "https://chat.example.com/api/webchat/health", "", "", 404},
	} {
		t.Run(tc.method+" "+tc.url+" "+tc.origin, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.url, nil)
			req.Header.Set("Origin", tc.origin)
			out := httptest.NewRecorder()
			router.ServeHTTP(out, req)
			if out.Code != tc.status || !strings.Contains(out.Body.String(), tc.contains) {
				t.Fatalf("got %d %s, want %d containing %q", out.Code, out.Body.String(), tc.status, tc.contains)
			}
			if tc.status == 405 && !strings.Contains(out.Header().Get("Allow"), "GET") {
				t.Fatal("missing Allow header")
			}
		})
	}
	// Registration and the returned cookie must work across the mounted API routes.
	req := httptest.NewRequest("POST", "https://chat.example.com/api/webchat/register", strings.NewReader(`{"name":"Alice","password":"strong-password","invite":"test-invitation-code"}`))
	req.Header.Set("Content-Type", "application/json")
	out := httptest.NewRecorder()
	router.ServeHTTP(out, req)
	if out.Code != 200 {
		t.Fatalf("register: %d %s", out.Code, out.Body.String())
	}
	cookies := out.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("missing session cookie")
	}
	req = httptest.NewRequest("GET", "https://chat.example.com/api/webchat/session", nil)
	req.AddCookie(cookies[0])
	out = httptest.NewRecorder()
	router.ServeHTTP(out, req)
	if out.Code != 200 || !strings.Contains(out.Body.String(), "Alice") {
		t.Fatalf("session: %d %s", out.Code, out.Body.String())
	}
}

func TestOptionalModulesAndLandingPage(t *testing.T) {
	for _, tc := range []struct {
		deps   Dependencies
		target string
	}{
		{Dependencies{}, ""},
		{Dependencies{Assistant: assistant.NewServer(nil)}, "/page/assistant/"},
	} {
		router := NewRouter(tc.deps)
		out := httptest.NewRecorder()
		router.ServeHTTP(out, httptest.NewRequest("GET", "http://localhost/", nil))
		if tc.target == "" {
			if out.Code != 404 {
				t.Fatalf("empty router: %d", out.Code)
			}
		} else if out.Code != http.StatusTemporaryRedirect || out.Header().Get("Location") != tc.target {
			t.Fatalf("landing: %d %s", out.Code, out.Header().Get("Location"))
		}
		for _, path := range []string{"/api/webchat/health", "/page/webchat/", "/api/feishu/events"} {
			out = httptest.NewRecorder()
			router.ServeHTTP(out, httptest.NewRequest("GET", "http://localhost"+path, nil))
			if out.Code != 404 {
				t.Fatalf("disabled route %s: %d", path, out.Code)
			}
		}
	}
}
