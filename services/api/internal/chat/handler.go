package chat

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"

	"github.com/LizHu95/Jetaime/services/api/internal/auth"
	"github.com/LizHu95/Jetaime/services/api/internal/httpapi"
)

type Handler struct {
	service        *Service
	authentication *auth.Handler
	realtime       http.Handler
}

func NewHandler(service *Service, authentication *auth.Handler, realtime http.Handler) *Handler {
	return &Handler{service, authentication, realtime}
}

// Authenticate checks the session before validating the group path parameter.
func (h *Handler) Authenticate(next http.HandlerFunc) http.HandlerFunc {
	return h.authentication.Authenticate(func(w http.ResponseWriter, r *http.Request) {
		if group := r.PathValue("group"); group != "" && !validID(group) {
			fail(w, 400, "invalid_input")
			return
		}
		next(w, r)
	})
}

// Events uses the realtime transport's own WebSocket authentication flow.
func (h *Handler) Events(w http.ResponseWriter, r *http.Request) {
	h.realtime.ServeHTTP(w, r)
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
		fail(w, 404, "group_not_found")
	case errors.Is(err, ErrAIUnavailable):
		fail(w, 503, "ai_unavailable")
	case errors.Is(err, ErrConflict):
		fail(w, 409, "message_conflict")
	default:
		fail(w, 500, "internal_error")
	}
}
func decode(w http.ResponseWriter, r *http.Request, target any) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		fail(w, 415, "unsupported_media_type")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32768)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	err = d.Decode(target)
	if err == nil {
		var extra any
		err = d.Decode(&extra)
		if errors.Is(err, io.EOF) {
			return true
		}
	}
	var large *http.MaxBytesError
	if errors.As(err, &large) {
		fail(w, 413, "request_too_large")
	} else {
		fail(w, 400, "invalid_json")
	}
	return false
}
func (h *Handler) GetState(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	state, err := h.service.GetState(r.Context(), r.PathValue("group"), u.ID)
	respond(w, state, err)
}
func (h *Handler) ListMessages(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	values := r.URL.Query()
	parse := func(key string) (int64, error) {
		if values.Get(key) == "" {
			return 0, nil
		}
		return strconv.ParseInt(values.Get(key), 10, 64)
	}
	after, e1 := parse("afterSeq")
	before, e2 := parse("beforeSeq")
	limit, e3 := parse("limit")
	if limit == 0 {
		limit = 100
	}
	if e1 != nil || e2 != nil || e3 != nil || after < 0 || before < 0 || (after > 0 && before > 0) || limit < 1 || limit > 100 {
		fail(w, 400, "invalid_input")
		return
	}
	page, err := h.service.ListMessages(r.Context(), r.PathValue("group"), u.ID, after, before, int(limit))
	respond(w, page, err)
}
func (h *Handler) SendMessage(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	var input SendInput
	if !decode(w, r, &input) {
		return
	}
	m, err := h.service.Send(r.Context(), r.PathValue("group"), u.ID, input)
	respond(w, m, err)
}
func (h *Handler) MarkRead(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	var input struct {
		Seq int64 `json:"seq"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.Seq < 0 {
		fail(w, 400, "invalid_input")
		return
	}
	seq, err := h.service.MarkRead(r.Context(), r.PathValue("group"), u.ID, input.Seq)
	respond(w, map[string]int64{"readSeq": seq}, err)
}
func (h *Handler) ListRuns(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	runs, err := h.service.ListRuns(r.Context(), r.PathValue("group"), u.ID)
	respond(w, runs, err)
}
func (h *Handler) RetryRun(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if !validID(r.PathValue("run")) {
		fail(w, 400, "invalid_input")
		return
	}
	err := h.service.RetryRun(r.Context(), r.PathValue("group"), u.ID, r.PathValue("run"))
	respond(w, map[string]bool{"ok": true}, err)
}
func (h *Handler) CloseGroup(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	err := h.service.CloseGroup(r.Context(), r.PathValue("group"), u.ID)
	respond(w, map[string]bool{"ok": true}, err)
}

func (h *Handler) GetMessage(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if !validID(r.PathValue("message")) {
		fail(w, 400, "invalid_input")
		return
	}
	message, err := h.service.GetMessage(r.Context(), r.PathValue("group"), u.ID, r.PathValue("message"))
	respond(w, message, err)
}

func (h *Handler) UpdateSettings(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ProactiveEnabled *bool `json:"proactiveEnabled"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.ProactiveEnabled == nil {
		fail(w, 400, "invalid_input")
		return
	}
	u := auth.UserFromContext(r.Context())
	state, err := h.service.UpdateSettings(r.Context(), r.PathValue("group"), u.ID, *input.ProactiveEnabled)
	respond(w, state, err)
}
