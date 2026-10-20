package auth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/httpapi"
)

type Handler struct{ service *Service }

func NewHandler(service *Service) *Handler { return &Handler{service: service} }

// Endpoints exports handlers; URL and HTTP method bindings live in httpapi/router.go.
func (h *Handler) Endpoints() *httpapi.AuthHandlers {
	return &httpapi.AuthHandlers{
		Register: http.HandlerFunc(h.register), Login: http.HandlerFunc(h.login), Logout: http.HandlerFunc(h.logout),
		ChangePassword: h.RequireUser(h.changePassword),
	}
}

type registerRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Nickname string `json:"nickname"`
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func readJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "use application/json")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8*1024)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	err = decoder.Decode(target)
	if err == nil {
		var extra any
		err = decoder.Decode(&extra)
		if errors.Is(err, io.EOF) {
			return true
		}
	}
	var sizeError *http.MaxBytesError
	if errors.As(err, &sizeError) {
		writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "request body exceeds 8 KiB")
	} else {
		writeError(w, http.StatusBadRequest, "invalid_json", "expected one JSON object with supported fields")
	}
	return false
}

func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	var request registerRequest
	if !readJSON(w, r, &request) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	user, err := h.service.Register(ctx, request.Username, request.Password, request.Nickname)
	if err != nil {
		respondError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, user)
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var request loginRequest
	if !readJSON(w, r, &request) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	result, err := h.service.Login(ctx, request.Username, request.Password)
	if err != nil {
		respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	values := r.Header.Values("Authorization")
	if len(values) != 1 {
		respondError(w, ErrUnauthorized)
		return
	}
	parts := strings.Fields(values[0])
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		respondError(w, ErrUnauthorized)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := h.service.Logout(ctx, parts[1]); err != nil {
		respondError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func respondError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInvalidInput):
		writeError(w, 400, "invalid_input", "invalid username, password or nickname")
	case errors.Is(err, ErrUsernameTaken):
		writeError(w, 409, "username_taken", ErrUsernameTaken.Error())
	case errors.Is(err, ErrCredentials):
		writeError(w, 401, "invalid_credentials", ErrCredentials.Error())
	case errors.Is(err, ErrUnauthorized):
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeError(w, 401, "unauthorized", ErrUnauthorized.Error())
	case errors.Is(err, ErrBusy):
		w.Header().Set("Retry-After", "1")
		writeError(w, 429, "auth_busy", ErrBusy.Error())
	default:
		writeError(w, 500, "internal_error", "authentication failed")
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
