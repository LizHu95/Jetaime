package httpapi_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/LizHu95/Jetaime/services/api/internal/auth"
	"github.com/LizHu95/Jetaime/services/api/internal/chat"
	"github.com/LizHu95/Jetaime/services/api/internal/collaboration"
	"github.com/LizHu95/Jetaime/services/api/internal/httpapi"
	"github.com/LizHu95/Jetaime/services/api/internal/notification/webpush"
	"github.com/LizHu95/Jetaime/services/api/internal/reminder"
)

func TestProtectedRoutesRejectAnonymousRequests(t *testing.T) {
	// Nil services make accidental dispatch past authentication fail immediately.
	accounts := auth.NewHandler(nil)
	router := httpapi.NewRouter(httpapi.Handlers{
		Auth:          accounts,
		Chat:          chat.NewHandler(nil, accounts, nil),
		Collaboration: collaboration.NewHandler(nil, accounts),
		Reminders:     reminder.NewHandler(nil, accounts),
		Push:          webpush.NewHandler(nil, accounts, webpush.Config{}),
	})
	for _, tc := range []struct{ method, path string }{
		{"POST", "/api/auth/password"},
		{"GET", "/api/contacts/users"},
		{"GET", "/api/contacts/requests"},
		{"POST", "/api/contacts/requests"},
		{"POST", "/api/contacts/requests/id/accept"},
		{"GET", "/api/groups"},
		{"GET", "/api/chat/groups/id"},
		{"GET", "/api/chat/groups/id/messages"},
		{"GET", "/api/chat/groups/id/messages/message"},
		{"POST", "/api/chat/groups/id/messages"},
		{"POST", "/api/chat/groups/id/read"},
		{"GET", "/api/chat/groups/id/runs"},
		{"POST", "/api/chat/groups/id/runs/run/retry"},
		{"POST", "/api/chat/groups/id/close"},
		{"GET", "/api/chat/groups/id/reminders"},
		{"POST", "/api/chat/groups/id/reminders"},
		{"PATCH", "/api/chat/groups/id/reminders/reminder"},
		{"POST", "/api/chat/groups/id/reminders/reminder/cancel"},
		{"POST", "/api/push/subscriptions"},
		{"DELETE", "/api/push/subscriptions"},
		{"POST", "/api/push/test"},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			out := httptest.NewRecorder()
			router.ServeHTTP(out, httptest.NewRequest(tc.method, tc.path, nil))
			if out.Code != http.StatusUnauthorized || out.Header().Get("WWW-Authenticate") != "Bearer" {
				t.Fatalf("anonymous request: %d %s", out.Code, out.Body.String())
			}
		})
	}
}
