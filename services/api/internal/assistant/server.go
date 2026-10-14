package assistant

import (
	"embed"
	"encoding/json"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

//go:embed web/*
var web embed.FS

type Server struct {
	workflow *Workflow
	handler  http.Handler
}

func NewServer(w *Workflow) *Server {
	s := &Server{workflow: w}
	mux := http.NewServeMux()
	assets, _ := fs.Sub(web, "web")
	mux.HandleFunc("GET /api/assistant/catalog", s.handleCatalog)
	mux.HandleFunc("GET /api/assistant/state", s.handleState)
	mux.HandleFunc("POST /api/assistant/run", s.withRequest(s.handleRun))
	mux.HandleFunc("POST /api/assistant/messages", s.withRequest(s.handleMessages))
	mux.HandleFunc("POST /api/assistant/reload", s.withRequest(s.handleReload))
	mux.HandleFunc("POST /api/assistant/dismiss", s.withRequest(s.handleDismiss))
	mux.HandleFunc("POST /api/assistant/confirm", s.withRequest(s.handleConfirm))
	mux.HandleFunc("POST /api/assistant/complete", s.withRequest(s.handleComplete))
	files := http.FileServer(http.FS(assets))
	for _, path := range []string{"/{$}", "/style.css", "/app.js"} {
		mux.Handle("GET "+path, files)
	}
	s.handler = mux
	return s
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		reply(w, 403, map[string]string{"error": "local host required"})
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Host != r.Host || (u.Scheme != "http" && u.Scheme != "https") {
			reply(w, 403, map[string]string{"error": "same origin required"})
			return
		}
	}
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		reply(w, 403, map[string]string{"error": "cross-site request rejected"})
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	s.handler.ServeHTTP(w, r)
}

type apiRequest struct {
	GroupID        string `json:"groupId"`
	ActorID        string `json:"actorId"`
	Kind           string `json:"kind"`
	Question       string `json:"question"`
	Model          string `json:"model"`
	Text           string `json:"text"`
	Key            string `json:"key"`
	Title          string `json:"title"`
	AssigneeID     string `json:"assigneeId"`
	DueAt          string `json:"dueAt"`
	TaskID         string `json:"taskId"`
	Done           bool   `json:"done"`
	AllowDuplicate bool   `json:"allowDuplicate"`
}

func (s *Server) withRequest(next func(http.ResponseWriter, *http.Request, apiRequest)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			reply(w, 415, map[string]string{"error": "JSON required"})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		defer func() { _ = r.Body.Close() }()
		var req apiRequest
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil {
			fail(w, 400, err)
			return
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			reply(w, 400, map[string]string{"error": "single JSON object required"})
			return
		}
		if _, err := s.workflow.Store.Context(req.GroupID, req.ActorID); err != nil {
			fail(w, 403, err)
			return
		}
		next(w, r, req)
	}
}

func (s *Server) handleCatalog(w http.ResponseWriter, r *http.Request) {
	store := s.workflow.Store
	reply(w, 200, map[string]any{"catalog": store.Catalog(), "modelDefault": s.workflow.DefaultModel})
	return
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	store := s.workflow.Store
	g, a := r.URL.Query().Get("groupId"), r.URL.Query().Get("actorId")
	c, err := store.Context(g, a)
	if err != nil {
		fail(w, 403, err)
		return
	}
	runs, err := s.workflow.Runs(g, a)
	if err != nil {
		fail(w, 403, err)
		return
	}
	reply(w, 200, map[string]any{"context": c, "runs": runs})
	return
}

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request, req apiRequest) {
	run, err := s.workflow.Execute(r.Context(), req.GroupID, req.ActorID, req.Kind, req.Question, req.Model)
	if err != nil {
		reply(w, 422, map[string]any{"error": err.Error(), "run": run})
		return
	}
	reply(w, 200, map[string]any{"run": run})
}

func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request, req apiRequest) {
	store := s.workflow.Store
	m, err := store.AppendMessage(req.GroupID, req.ActorID, req.Text)
	if err != nil {
		fail(w, 400, err)
		return
	}
	reply(w, 200, m)
}

func (s *Server) handleReload(w http.ResponseWriter, r *http.Request, req apiRequest) {
	store := s.workflow.Store
	if err := store.Reload(); err != nil {
		fail(w, 400, err)
		return
	}
	reply(w, 200, map[string]bool{"ok": true})
}

func (s *Server) handleDismiss(w http.ResponseWriter, r *http.Request, req apiRequest) {
	if err := s.workflow.Dismiss(req.GroupID, req.ActorID, req.Key); err != nil {
		fail(w, 400, err)
		return
	}
	reply(w, 200, map[string]bool{"ok": true})
}

func (s *Server) handleConfirm(w http.ResponseWriter, r *http.Request, req apiRequest) {
	store := s.workflow.Store
	existing, err := store.FindConfirmation(req.GroupID, req.ActorID, req.Key)
	if err != nil {
		fail(w, 400, err)
		return
	}
	if existing != nil {
		reply(w, 200, existing)
		return
	}
	suggestion, err := s.workflow.Suggestion(req.GroupID, req.ActorID, req.Key)
	if err != nil {
		fail(w, 400, err)
		return
	}
	c, err := store.Context(req.GroupID, req.ActorID)
	if err != nil {
		fail(w, 403, err)
		return
	}
	for _, t := range c.Tasks {
		if (sameSources(t.SourceMessageIDs, suggestion.SourceMessageIDs) || strings.TrimSpace(t.Title) == strings.TrimSpace(req.Title)) && !req.AllowDuplicate {
			reply(w, 409, map[string]string{"error": "可能重复，请核对已有待办，或勾选仍然创建"})
			return
		}
	}
	var due *time.Time
	if req.DueAt != "" {
		zone, err := time.LoadLocation("Asia/Shanghai")
		if err != nil {
			fail(w, 500, err)
			return
		}
		date, err := time.ParseInLocation("2006-01-02", req.DueAt, zone)
		if err != nil {
			fail(w, 400, err)
			return
		}
		due = &date
	}
	task, err := store.Confirm(req.GroupID, req.ActorID, req.Key, suggestion, req.Title, req.AssigneeID, due)
	if err != nil {
		fail(w, 400, err)
		return
	}
	reply(w, 200, task)
}

func (s *Server) handleComplete(w http.ResponseWriter, r *http.Request, req apiRequest) {
	store := s.workflow.Store
	task, err := store.Complete(req.GroupID, req.ActorID, req.TaskID, req.Done)
	if err != nil {
		fail(w, 400, err)
		return
	}
	reply(w, 200, task)
}

func reply(w http.ResponseWriter, status int, value any) {
	data, err := json.Marshal(value)
	if err != nil {
		status = http.StatusInternalServerError
		data, _ = json.Marshal(map[string]string{"error": "response encoding failed: " + err.Error()})
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(append(data, '\n'))
}

func fail(w http.ResponseWriter, status int, err error) {
	reply(w, status, map[string]string{"error": err.Error()})
}

// RegisterRoutes mounts this module with its local-access checks intact.
func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	mux.Handle("/api/assistant/", s)
	mux.Handle("/page/assistant/", http.StripPrefix("/page/assistant", s))
}
