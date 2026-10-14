// Package httpapi assembles HTTP modules without owning their business logic.
package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// Module registers its namespaced API and page routes, including access checks.
type Module interface {
	RegisterRoutes(*http.ServeMux)
}

type Dependencies struct {
	Assistant Module
	Webchat   Module
}

func NewRouter(deps Dependencies) http.Handler {
	mux := http.NewServeMux()
	if deps.Assistant != nil {
		deps.Assistant.RegisterRoutes(mux)
	}
	if deps.Webchat != nil {
		deps.Webchat.RegisterRoutes(mux)
	}
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		target := "/page/assistant/"
		if deps.Webchat != nil {
			target = "/page/webchat/"
		} else if deps.Assistant == nil {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, target, http.StatusTemporaryRedirect)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if value := recover(); value != nil {
				slog.ErrorContext(r.Context(), "HTTP handler panic", "method", r.Method, "path", r.URL.Path, "panic", value)
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		mux.ServeHTTP(w, r)
	})
}
