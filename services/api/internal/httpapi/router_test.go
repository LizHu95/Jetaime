package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type testModule struct {
	path string
}

func (m testModule) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET "+m.path, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
}

func TestConnectivityOnly(t *testing.T) {
	router := NewRouter()
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

func TestInjectedModules(t *testing.T) {
	router := NewRouter(testModule{path: "/api/first"}, testModule{path: "/page/second/"})
	for _, path := range []string{"/api/first", "/page/second/"} {
		out := httptest.NewRecorder()
		router.ServeHTTP(out, httptest.NewRequest("GET", path, nil))
		if out.Code != 204 {
			t.Fatalf("module %s was not registered: %d", path, out.Code)
		}
	}
}
