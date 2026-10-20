package collaboration

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/LizHu95/Jetaime/services/api/internal/auth"
	"github.com/LizHu95/Jetaime/services/api/internal/httpapi"
)

type Handler struct {
	service *Service
	auth    *auth.Handler
}

func NewHandler(service *Service, authentication *auth.Handler) *Handler {
	return &Handler{service: service, auth: authentication}
}
func (h *Handler) Authenticate(next http.HandlerFunc) http.HandlerFunc {
	return h.auth.Authenticate(next)
}

func write(w http.ResponseWriter, status int, value any) {
	httpapi.WriteJSON(w, status, value)
}
func fail(w http.ResponseWriter, status int, code string) {
	write(w, status, map[string]any{"error": map[string]string{"code": code, "message": code}})
}
func respond(w http.ResponseWriter, value any, err error) {
	switch {
	case err == nil:
		write(w, 200, value)
	case errors.Is(err, ErrInvalid):
		fail(w, 400, "invalid_input")
	case errors.Is(err, ErrNotFound):
		fail(w, 404, "not_found")
	case errors.Is(err, ErrForbidden):
		fail(w, 403, "forbidden")
	case errors.Is(err, ErrConflict):
		fail(w, 409, "request_conflict")
	case errors.Is(err, ErrConnected):
		fail(w, 409, "already_connected")
	default:
		fail(w, 500, "internal_error")
	}
}
func (h *Handler) FindUser(w http.ResponseWriter, r *http.Request) {
	u, err := h.service.FindUser(r.Context(), r.URL.Query().Get("username"))
	respond(w, u, err)
}
func (h *Handler) ListRequests(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	result, err := h.service.ListRequests(r.Context(), u.ID)
	respond(w, result, err)
}
func (h *Handler) ListGroups(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	result, err := h.service.ListGroups(r.Context(), u.ID)
	respond(w, result, err)
}
func (h *Handler) ResolveRequest(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	result, err := h.service.Resolve(r.Context(), u.ID, r.PathValue("id"), r.PathValue("action"))
	respond(w, result, err)
}
func (h *Handler) SendRequest(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		fail(w, 415, "unsupported_media_type")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var input struct {
		Username string `json:"username"`
	}
	err = decoder.Decode(&input)
	if err == nil {
		var extra any
		err = decoder.Decode(&extra)
		if errors.Is(err, io.EOF) {
			result, e := h.service.Send(r.Context(), u.ID, input.Username)
			respond(w, result, e)
			return
		}
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		fail(w, 413, "request_too_large")
	} else {
		fail(w, 400, "invalid_json")
	}
}
