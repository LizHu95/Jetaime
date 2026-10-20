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
		mux.HandleFunc("POST /api/auth/register", h.Register)
		mux.HandleFunc("POST /api/auth/login", h.Login)
		mux.HandleFunc("POST /api/auth/logout", h.Logout)
		mux.HandleFunc("POST /api/auth/password", h.Authenticate(h.ChangePassword))
	}
	if h := handlers.Collaboration; h != nil {
		mux.HandleFunc("GET /api/contacts/users", h.Authenticate(h.FindUser))
		mux.HandleFunc("GET /api/contacts/requests", h.Authenticate(h.ListRequests))
		mux.HandleFunc("POST /api/contacts/requests", h.Authenticate(h.SendRequest))
		mux.HandleFunc("POST /api/contacts/requests/{id}/{action}", h.Authenticate(h.ResolveRequest))
		mux.HandleFunc("GET /api/groups", h.Authenticate(h.ListGroups))
	}
	if h := handlers.Chat; h != nil {
		mux.HandleFunc("GET /api/chat/groups/{group}", h.Authenticate(h.GetState))
		mux.HandleFunc("GET /api/chat/groups/{group}/messages", h.Authenticate(h.ListMessages))
		mux.HandleFunc("GET /api/chat/groups/{group}/messages/{message}", h.Authenticate(h.GetMessage))
		mux.HandleFunc("POST /api/chat/groups/{group}/messages", h.Authenticate(h.SendMessage))
		mux.HandleFunc("POST /api/chat/groups/{group}/read", h.Authenticate(h.MarkRead))
		mux.HandleFunc("GET /api/chat/groups/{group}/runs", h.Authenticate(h.ListRuns))
		mux.HandleFunc("POST /api/chat/groups/{group}/runs/{run}/retry", h.Authenticate(h.RetryRun))
		mux.HandleFunc("POST /api/chat/groups/{group}/close", h.Authenticate(h.CloseGroup))
		mux.HandleFunc("GET /api/chat/events", h.Events)
	}
	if h := handlers.Reminders; h != nil {
		mux.HandleFunc("GET /api/chat/groups/{group}/reminders", h.Authenticate(h.ListReminders))
		mux.HandleFunc("POST /api/chat/groups/{group}/reminders", h.Authenticate(h.CreateReminder))
		mux.HandleFunc("PATCH /api/chat/groups/{group}/reminders/{reminder}", h.Authenticate(h.UpdateReminder))
		mux.HandleFunc("POST /api/chat/groups/{group}/reminders/{reminder}/cancel", h.Authenticate(h.CancelReminder))
	}
	if h := handlers.Push; h != nil {
		mux.HandleFunc("GET /api/push/public-key", h.GetPublicKey)
		mux.HandleFunc("POST /api/push/subscriptions", h.Authenticate(h.Subscribe))
		mux.HandleFunc("DELETE /api/push/subscriptions", h.Authenticate(h.Unsubscribe))
		mux.HandleFunc("POST /api/push/test", h.Authenticate(h.SendTest))
	}
	if h := handlers.Assistant; h != nil {
		mux.Handle("GET /api/assistant/catalog", h.LocalOnly(http.HandlerFunc(h.GetCatalog)))
		mux.Handle("GET /api/assistant/state", h.LocalOnly(http.HandlerFunc(h.GetState)))
		mux.Handle("POST /api/assistant/run", h.LocalOnly(http.HandlerFunc(h.Run)))
		mux.Handle("POST /api/assistant/messages", h.LocalOnly(http.HandlerFunc(h.SendMessage)))
		mux.Handle("POST /api/assistant/reload", h.LocalOnly(http.HandlerFunc(h.Reload)))
		mux.Handle("POST /api/assistant/dismiss", h.LocalOnly(http.HandlerFunc(h.Dismiss)))
		mux.Handle("POST /api/assistant/confirm", h.LocalOnly(http.HandlerFunc(h.Confirm)))
		mux.Handle("POST /api/assistant/complete", h.LocalOnly(http.HandlerFunc(h.Complete)))
		mux.Handle("GET /page/assistant/{$}", h.LocalOnly(http.HandlerFunc(h.Page)))
		mux.Handle("GET /page/assistant/style.css", h.LocalOnly(http.HandlerFunc(h.Page)))
		mux.Handle("GET /page/assistant/app.js", h.LocalOnly(http.HandlerFunc(h.Page)))
	}
	return mux
}
