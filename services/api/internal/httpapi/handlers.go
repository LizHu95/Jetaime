package httpapi

import "net/http"

// Handlers contains enabled HTTP modules. Leave disabled modules nil.
// Interfaces keep the route table independent of domain implementations;
// each method is a named HTTP handler, not an endpoint field mapping.
type Handlers struct {
	Auth          AuthHandler
	Collaboration CollaborationHandler
	Chat          ChatHandler
	Reminders     ReminderHandler
	Push          PushHandler
}

// Authenticated supplies the session middleware used explicitly in the route table.
type Authenticated interface {
	Authenticate(http.HandlerFunc) http.HandlerFunc
}

type AuthHandler interface {
	Authenticated
	Register(http.ResponseWriter, *http.Request)
	Login(http.ResponseWriter, *http.Request)
	Logout(http.ResponseWriter, *http.Request)
	ChangePassword(http.ResponseWriter, *http.Request)
}

type CollaborationHandler interface {
	Authenticated
	FindUser(http.ResponseWriter, *http.Request)
	ListRequests(http.ResponseWriter, *http.Request)
	SendRequest(http.ResponseWriter, *http.Request)
	ResolveRequest(http.ResponseWriter, *http.Request)
	ListGroups(http.ResponseWriter, *http.Request)
}

type ChatHandler interface {
	UpdateSettings(http.ResponseWriter, *http.Request)
	Authenticated
	GetState(http.ResponseWriter, *http.Request)
	ListMessages(http.ResponseWriter, *http.Request)
	GetMessage(http.ResponseWriter, *http.Request)
	SendMessage(http.ResponseWriter, *http.Request)
	MarkRead(http.ResponseWriter, *http.Request)
	ListRuns(http.ResponseWriter, *http.Request)
	RetryRun(http.ResponseWriter, *http.Request)
	CloseGroup(http.ResponseWriter, *http.Request)
	Events(http.ResponseWriter, *http.Request)
}

type PushHandler interface {
	Authenticated
	GetPublicKey(http.ResponseWriter, *http.Request)
	Subscribe(http.ResponseWriter, *http.Request)
	Unsubscribe(http.ResponseWriter, *http.Request)
	SendTest(http.ResponseWriter, *http.Request)
}

type ReminderHandler interface {
	Authenticated
	CreateReminder(http.ResponseWriter, *http.Request)
	UpdateReminder(http.ResponseWriter, *http.Request)
	ListReminders(http.ResponseWriter, *http.Request)
	CancelReminder(http.ResponseWriter, *http.Request)
}
