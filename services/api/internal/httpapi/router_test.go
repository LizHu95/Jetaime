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
	endpoint := func(name string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Endpoint", name)
			w.Header().Set("Group", r.PathValue("group"))
			w.Header().Set("Message", r.PathValue("message"))
			w.WriteHeader(http.StatusNoContent)
		})
	}
	routes := &ChatHandlers{State: endpoint("state"), Messages: endpoint("messages"), Message: endpoint("message"), Send: endpoint("send"), Read: endpoint("read"), Runs: endpoint("runs"), Retry: endpoint("retry"), Close: endpoint("close"), Events: endpoint("events")}
	router := NewRouter(Handlers{Chat: routes})
	for _, tc := range []struct{ method, path, endpoint string }{
		{"GET", "/api/chat/groups/group-id/messages/message-id", "message"},
		{"GET", "/api/chat/groups/group-id/messages", "messages"},
		{"POST", "/api/chat/groups/group-id/messages", "send"},
		{"GET", "/api/chat/events", "events"},
	} {
		out := httptest.NewRecorder()
		router.ServeHTTP(out, httptest.NewRequest(tc.method, tc.path, nil))
		if out.Code != 204 || out.Header().Get("Endpoint") != tc.endpoint {
			t.Fatalf("wrong binding for %s %s: %d %s", tc.method, tc.path, out.Code, out.Header().Get("Endpoint"))
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
