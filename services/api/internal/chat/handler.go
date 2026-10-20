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
func (h *Handler) Endpoints() *httpapi.ChatHandlers {
	wrap := func(next func(http.ResponseWriter, *http.Request, auth.User)) http.HandlerFunc {
		return h.authentication.RequireUser(func(w http.ResponseWriter, r *http.Request, u auth.User) {
			if group := r.PathValue("group"); group != "" && !validID(group) {
				fail(w, 400, "invalid_input")
				return
			}
			next(w, r, u)
		})
	}
	return &httpapi.ChatHandlers{State: wrap(h.state), Messages: wrap(h.messages), Message: wrap(h.message), Send: wrap(h.send), Read: wrap(h.read), Runs: wrap(h.runs), Retry: wrap(h.retry), Close: wrap(h.close), Events: h.realtime}
}
func write(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
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
func (h *Handler) state(w http.ResponseWriter, r *http.Request, u auth.User) {
	state, err := h.service.store.State(r.Context(), r.PathValue("group"), u.ID)
	state.AIEnabled = h.service.aiEnabled
	respond(w, state, err)
}
func (h *Handler) messages(w http.ResponseWriter, r *http.Request, u auth.User) {
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
	page, err := h.service.store.Messages(r.Context(), r.PathValue("group"), u.ID, after, before, int(limit))
	respond(w, page, err)
}
func (h *Handler) send(w http.ResponseWriter, r *http.Request, u auth.User) {
	var input SendInput
	if !decode(w, r, &input) {
		return
	}
	m, err := h.service.Send(r.Context(), r.PathValue("group"), u.ID, input)
	respond(w, m, err)
}
func (h *Handler) read(w http.ResponseWriter, r *http.Request, u auth.User) {
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
	seq, err := h.service.store.Read(r.Context(), r.PathValue("group"), u.ID, input.Seq)
	respond(w, map[string]int64{"readSeq": seq}, err)
}
func (h *Handler) runs(w http.ResponseWriter, r *http.Request, u auth.User) {
	runs, err := h.service.store.Runs(r.Context(), r.PathValue("group"), u.ID)
	respond(w, runs, err)
}
func (h *Handler) retry(w http.ResponseWriter, r *http.Request, u auth.User) {
	if !validID(r.PathValue("run")) {
		fail(w, 400, "invalid_input")
		return
	}
	if !h.service.aiEnabled {
		fail(w, 503, "ai_unavailable")
		return
	}
	err := h.service.store.Retry(r.Context(), r.PathValue("group"), u.ID, r.PathValue("run"))
	respond(w, map[string]bool{"ok": true}, err)
}
func (h *Handler) close(w http.ResponseWriter, r *http.Request, u auth.User) {
	err := h.service.store.Close(r.Context(), r.PathValue("group"), u.ID)
	respond(w, map[string]bool{"ok": true}, err)
}

func (h *Handler) message(w http.ResponseWriter, r *http.Request, u auth.User) {
	if !validID(r.PathValue("message")) {
		fail(w, 400, "invalid_input")
		return
	}
	message, err := h.service.store.Message(r.Context(), r.PathValue("group"), u.ID, r.PathValue("message"))
	respond(w, message, err)
}
