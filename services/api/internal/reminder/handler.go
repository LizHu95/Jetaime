package reminder

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/LizHu95/Jetaime/services/api/internal/auth"
	"github.com/LizHu95/Jetaime/services/api/internal/httpapi"
	"github.com/google/uuid"
)

type Handler struct {
	service        *Service
	authentication *auth.Handler
}

func NewHandler(service *Service, authentication *auth.Handler) *Handler {
	return &Handler{service: service, authentication: authentication}
}

func (h *Handler) Authenticate(next http.HandlerFunc) http.HandlerFunc {
	return h.authentication.Authenticate(func(w http.ResponseWriter, r *http.Request) {
		if _, err := uuid.Parse(r.PathValue("group")); err != nil {
			respond(w, nil, 400, "invalid_input")
			return
		}
		next(w, r)
	})
}

func (h *Handler) CreateReminder(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	var input struct {
		OperationID string `json:"operationId"`
		CreateInput
	}
	if !decode(w, r, &input) {
		return
	}
	value, err := h.service.Create(r.Context(), Scope{GroupID: r.PathValue("group"), UserID: u.ID}, input.CreateInput, Operation{ID: input.OperationID})
	result(w, value, err)
}

func (h *Handler) UpdateReminder(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	var input struct {
		OperationID string `json:"operationId"`
		UpdateInput
	}
	if !decode(w, r, &input) {
		return
	}
	value, err := h.service.Update(r.Context(), Scope{GroupID: r.PathValue("group"), UserID: u.ID}, r.PathValue("reminder"), input.UpdateInput, Operation{ID: input.OperationID})
	result(w, value, err)
}

func (h *Handler) ListReminders(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	list, err := h.service.List(r.Context(), Scope{GroupID: r.PathValue("group"), UserID: u.ID})
	result(w, list, err)
}

func (h *Handler) CancelReminder(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if _, err := uuid.Parse(r.PathValue("reminder")); err != nil {
		respond(w, nil, 400, "invalid_input")
		return
	}
	err := h.service.Cancel(r.Context(), Scope{GroupID: r.PathValue("group"), UserID: u.ID}, r.PathValue("reminder"))
	result(w, map[string]bool{"ok": true}, err)
}
func result(w http.ResponseWriter, value any, err error) {
	if errors.Is(err, ErrNotFound) {
		respond(w, nil, 404, "reminder_not_found")
	} else if errors.Is(err, ErrInvalid) {
		respond(w, nil, 400, "invalid_input")
	} else if errors.Is(err, ErrConflict) {
		respond(w, nil, 409, "reminder_conflict")
	} else if err != nil {
		respond(w, nil, 500, "internal_error")
	} else {
		respond(w, value, 200, "")
	}
}
func respond(w http.ResponseWriter, value any, status int, code string) {
	if code != "" {
		value = map[string]any{"error": map[string]string{"code": code, "message": code}}
	}
	httpapi.WriteJSON(w, status, value)
}

func decode(w http.ResponseWriter, r *http.Request, value any) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		respond(w, nil, 415, "unsupported_media_type")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	err = decoder.Decode(value)
	if err == nil {
		var extra any
		if errors.Is(decoder.Decode(&extra), io.EOF) {
			return true
		}
	}
	respond(w, nil, 400, "invalid_json")
	return false
}
