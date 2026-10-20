// Package httpapi owns the complete HTTP route table, without business dependencies.
package httpapi

import (
	"encoding/json"
	"net/http"
)

// NewRouter is the only route registration entry point. See app/server.go for
// endpoint construction and module enablement; business modules contain no paths.
func NewRouter(handlers Handlers) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /test/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "message": "hello"})
	})

	if h := handlers.Auth; h != nil {
		mux.Handle("POST /api/auth/register", h.Register)
		mux.Handle("POST /api/auth/login", h.Login)
		mux.Handle("POST /api/auth/logout", h.Logout)
		mux.Handle("GET /api/auth/me", h.Me)
		mux.Handle("POST /api/auth/password", h.ChangePassword)
	}
	if h := handlers.Collaboration; h != nil {
		mux.Handle("GET /api/contacts/users", h.FindUser)
		mux.Handle("GET /api/contacts/requests", h.Requests)
		mux.Handle("POST /api/contacts/requests", h.Send)
		mux.Handle("POST /api/contacts/requests/{id}/{action}", h.Resolve)
		mux.Handle("GET /api/groups", h.Groups)
	}
	if h := handlers.Chat; h != nil {
		mux.Handle("GET /api/chat/groups/{group}", h.State)
		mux.Handle("GET /api/chat/groups/{group}/messages", h.Messages)
		mux.Handle("GET /api/chat/groups/{group}/messages/{message}", h.Message)
		mux.Handle("POST /api/chat/groups/{group}/messages", h.Send)
		mux.Handle("POST /api/chat/groups/{group}/read", h.Read)
		mux.Handle("GET /api/chat/groups/{group}/runs", h.Runs)
		mux.Handle("POST /api/chat/groups/{group}/runs/{run}/retry", h.Retry)
		mux.Handle("POST /api/chat/groups/{group}/close", h.Close)
		mux.Handle("GET /api/chat/events", h.Events)
	}
	if h := handlers.Push; h != nil {
		mux.Handle("GET /api/push/public-key", h.PublicKey)
		mux.Handle("POST /api/push/subscriptions", h.Subscribe)
		mux.Handle("DELETE /api/push/subscriptions", h.Remove)
		mux.Handle("POST /api/push/test", h.Test)
	}
	if h := handlers.Assistant; h != nil {
		mux.Handle("GET /api/assistant/catalog", h.Catalog)
		mux.Handle("GET /api/assistant/state", h.State)
		mux.Handle("POST /api/assistant/run", h.Run)
		mux.Handle("POST /api/assistant/messages", h.Messages)
		mux.Handle("POST /api/assistant/reload", h.Reload)
		mux.Handle("POST /api/assistant/dismiss", h.Dismiss)
		mux.Handle("POST /api/assistant/confirm", h.Confirm)
		mux.Handle("POST /api/assistant/complete", h.Complete)
		mux.Handle("GET /page/assistant/{$}", h.Page)
		mux.Handle("GET /page/assistant/style.css", h.Page)
		mux.Handle("GET /page/assistant/app.js", h.Page)
	}
	return mux
}
