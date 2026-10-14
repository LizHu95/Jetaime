// Package httpapi assembles HTTP modules without owning their business logic.
package httpapi

import (
	"encoding/json"
	"net/http"
)

// Module registers its namespaced API and page routes, including access checks.
type Module interface {
	RegisterRoutes(*http.ServeMux)
}

// NewRouter mounts the connectivity test and explicitly injected modules.
func NewRouter(modules ...Module) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /test/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "message": "hello"})
	})
	for _, module := range modules {
		module.RegisterRoutes(mux)
	}
	return mux
}
