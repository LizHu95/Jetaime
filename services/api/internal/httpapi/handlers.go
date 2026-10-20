package httpapi

import "net/http"

// Handlers contains enabled endpoint groups. A nil group mounts no routes.
// Modules supply handlers with their authentication and validation already applied.
type Handlers struct {
	Auth          *AuthHandlers
	Collaboration *CollaborationHandlers
	Chat          *ChatHandlers
	Push          *PushHandlers
	Assistant     *AssistantHandlers
}
type AuthHandlers struct {
	Register       http.Handler
	Login          http.Handler
	Logout         http.Handler
	ChangePassword http.Handler
}

type CollaborationHandlers struct {
	FindUser http.Handler
	Requests http.Handler
	Send     http.Handler
	Resolve  http.Handler
	Groups   http.Handler
}

type ChatHandlers struct {
	State    http.Handler
	Messages http.Handler
	Message  http.Handler
	Send     http.Handler
	Read     http.Handler
	Runs     http.Handler
	Retry    http.Handler
	Close    http.Handler
	Events   http.Handler
}

type PushHandlers struct {
	PublicKey http.Handler
	Subscribe http.Handler
	Remove    http.Handler
	Test      http.Handler
}

type AssistantHandlers struct {
	Catalog  http.Handler
	State    http.Handler
	Run      http.Handler
	Messages http.Handler
	Reload   http.Handler
	Dismiss  http.Handler
	Confirm  http.Handler
	Complete http.Handler
	Page     http.Handler
}
