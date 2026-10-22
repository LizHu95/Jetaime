package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConnectivityOnly(t *testing.T) {
	router := NewRouter(Handlers{})
	out := httptest.NewRecorder()
	router.ServeHTTP(out, httptest.NewRequest("GET", "/test/healthz", nil))
	if out.Code != 200 || out.Body.String() != "{\"message\":\"hello\",\"status\":\"ok\"}\n" || !strings.HasPrefix(out.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("connectivity: %d %s", out.Code, out.Body.String())
	}
	for _, path := range []string{"/", "/api/health", "/api/ready", "/api/assistant/catalog", "/page/assistant/"} {
		out := httptest.NewRecorder()
		router.ServeHTTP(out, httptest.NewRequest("GET", path, nil))
		if out.Code != 404 {
			t.Fatalf("unexpected built-in route %s: %d", path, out.Code)
		}
	}
	out = httptest.NewRecorder()
	router.ServeHTTP(out, httptest.NewRequest("POST", "/test/healthz", nil))
	if out.Code != 405 || !strings.Contains(out.Header().Get("Allow"), "GET") {
		t.Fatalf("method restriction: %d %s", out.Code, out.Header().Get("Allow"))
	}
}

func TestCentralRouteBindingsAndPathValues(t *testing.T) {
	router := NewRouter(Handlers{Chat: &routeChat{}})
	for _, tc := range []struct{ method, path, endpoint string }{
		{"GET", "/api/chat/groups/group-id/messages/message-id", "message"},
		{"GET", "/api/chat/groups/group-id/messages", "messages"},
		{"POST", "/api/chat/groups/group-id/messages", "send"},
		{"GET", "/api/chat/events", "events"},
		{"GET", "/api/chat/groups/group-id", "state"},
		{"POST", "/api/chat/groups/group-id/read", "read"},
		{"GET", "/api/chat/groups/group-id/runs", "runs"},
		{"POST", "/api/chat/groups/group-id/runs/run-id/retry", "retry"},
		{"POST", "/api/chat/groups/group-id/close", "close"},
		{"POST", "/api/chat/groups/group-id/settings", "settings"},
	} {
		out := httptest.NewRecorder()
		router.ServeHTTP(out, httptest.NewRequest(tc.method, tc.path, nil))
		if out.Code != 204 || out.Header().Get("Endpoint") != tc.endpoint {
			t.Fatalf("wrong binding for %s %s: %d %s", tc.method, tc.path, out.Code, out.Header().Get("Endpoint"))
		}
		if got := out.Header().Get("Authenticated"); (got == "true") != (tc.endpoint != "events") {
			t.Fatalf("authentication wrapper for %s: %q", tc.path, got)
		}
		if strings.Contains(tc.path, "group-id") && out.Header().Get("Group") != "group-id" {
			t.Fatal("group path value missing")
		}
		if tc.endpoint == "message" && out.Header().Get("Message") != "message-id" {
			t.Fatal("message path value missing")
		}
	}
	out := httptest.NewRecorder()
	router.ServeHTTP(out, httptest.NewRequest("DELETE", "/api/chat/groups/group-id/messages", nil))
	if out.Code != 405 || !strings.Contains(out.Header().Get("Allow"), "POST") {
		t.Fatal("HTTP method restrictions lost", out.Code)
	}
	out = httptest.NewRecorder()
	router.ServeHTTP(out, httptest.NewRequest("POST", "/api/auth/login", nil))
	if out.Code != 404 {
		t.Fatal("disabled endpoint group was mounted", out.Code)
	}
}

// routeChat exercises route dispatch and middleware without database dependencies.
type routeChat struct{}

func (*routeChat) UpdateSettings(w http.ResponseWriter, r *http.Request) {
	routeEndpoint(w, r, "settings")
}

func (*routeChat) Authenticate(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Reject-Authentication") != "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Authenticated", "true")
		next(w, r)
	}
}

func routeEndpoint(w http.ResponseWriter, r *http.Request, name string) {
	w.Header().Set("Endpoint", name)
	w.Header().Set("Group", r.PathValue("group"))
	w.Header().Set("Message", r.PathValue("message"))
	w.WriteHeader(http.StatusNoContent)
}

func (*routeChat) GetState(w http.ResponseWriter, r *http.Request) { routeEndpoint(w, r, "state") }

func (*routeChat) ListMessages(w http.ResponseWriter, r *http.Request) {
	routeEndpoint(w, r, "messages")
}

func (*routeChat) GetMessage(w http.ResponseWriter, r *http.Request) { routeEndpoint(w, r, "message") }

func (*routeChat) SendMessage(w http.ResponseWriter, r *http.Request) { routeEndpoint(w, r, "send") }

func (*routeChat) MarkRead(w http.ResponseWriter, r *http.Request) { routeEndpoint(w, r, "read") }

func (*routeChat) ListRuns(w http.ResponseWriter, r *http.Request) { routeEndpoint(w, r, "runs") }

func (*routeChat) RetryRun(w http.ResponseWriter, r *http.Request) { routeEndpoint(w, r, "retry") }

func (*routeChat) CloseGroup(w http.ResponseWriter, r *http.Request) { routeEndpoint(w, r, "close") }

func (*routeChat) Events(w http.ResponseWriter, r *http.Request) { routeEndpoint(w, r, "events") }

func TestAuthenticationStopsDispatch(t *testing.T) {
	router := NewRouter(Handlers{Chat: &routeChat{}})
	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/chat/groups/group-id"},
		{"GET", "/api/chat/groups/group-id/messages"},
		{"GET", "/api/chat/groups/group-id/messages/message-id"},
		{"POST", "/api/chat/groups/group-id/messages"},
		{"POST", "/api/chat/groups/group-id/read"},
		{"GET", "/api/chat/groups/group-id/runs"},
		{"POST", "/api/chat/groups/group-id/runs/run-id/retry"},
		{"POST", "/api/chat/groups/group-id/close"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.Header.Set("Reject-Authentication", "true")
		out := httptest.NewRecorder()
		router.ServeHTTP(out, req)
		if out.Code != http.StatusUnauthorized || out.Header().Get("Endpoint") != "" {
			t.Fatalf("unauthenticated handler reached for %s %s: %d", tc.method, tc.path, out.Code)
		}
	}
}
