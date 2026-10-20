package httpapi

import (
	"encoding/json"
	"net/http"
)

// WriteJSON is the common response writer for account and domain APIs.
// Domain handlers retain responsibility for mapping business errors to status codes.
func WriteJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if status != http.StatusNoContent {
		_ = json.NewEncoder(w).Encode(value)
	}
}
