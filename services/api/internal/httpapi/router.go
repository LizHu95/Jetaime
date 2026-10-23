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
		mux.HandleFunc("POST /api/chat/groups/{group}/settings", h.Authenticate(h.UpdateSettings))
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
	return mux
}
