package debugconsole

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
}

func NewServer(w *Workflow) *Server { return &Server{workflow: w} }
func (s *Server) Page(w http.ResponseWriter, r *http.Request) {
	assets, _ := fs.Sub(web, "web")
	http.StripPrefix("/page/assistant", http.FileServer(http.FS(assets))).ServeHTTP(w, r)
}
func (s *Server) LocalOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		next.ServeHTTP(w, r)
	})
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

func (s *Server) readRequest(w http.ResponseWriter, r *http.Request) (apiRequest, bool) {
	var req apiRequest
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		reply(w, 415, map[string]string{"error": "JSON required"})
		return req, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	defer func() { _ = r.Body.Close() }()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		fail(w, 400, err)
		return req, false
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		reply(w, 400, map[string]string{"error": "single JSON object required"})
		return req, false
	}
	if _, err := s.workflow.Store.Context(req.GroupID, req.ActorID); err != nil {
		fail(w, 403, err)
		return req, false
	}
	return req, true
}

func (s *Server) GetCatalog(w http.ResponseWriter, r *http.Request) {
	store := s.workflow.Store
	reply(w, 200, map[string]any{"catalog": store.Catalog(), "modelDefault": s.workflow.DefaultModel})
	return
}

func (s *Server) GetState(w http.ResponseWriter, r *http.Request) {
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

func (s *Server) Run(w http.ResponseWriter, r *http.Request) {
	req, ok := s.readRequest(w, r)
	if !ok {
		return
	}
	run, err := s.workflow.Execute(r.Context(), req.GroupID, req.ActorID, req.Kind, req.Question, req.Model)
	if err != nil {
		reply(w, 422, map[string]any{"error": err.Error(), "run": run})
		return
	}
	reply(w, 200, map[string]any{"run": run})
}

func (s *Server) SendMessage(w http.ResponseWriter, r *http.Request) {
	req, ok := s.readRequest(w, r)
	if !ok {
		return
	}
	store := s.workflow.Store
	m, err := store.AppendMessage(req.GroupID, req.ActorID, req.Text)
	if err != nil {
		fail(w, 400, err)
		return
	}
	reply(w, 200, m)
}

func (s *Server) Reload(w http.ResponseWriter, r *http.Request) {
	_, ok := s.readRequest(w, r)
	if !ok {
		return
	}
	store := s.workflow.Store
	if err := store.Reload(); err != nil {
		fail(w, 400, err)
		return
	}
	reply(w, 200, map[string]bool{"ok": true})
}

func (s *Server) Dismiss(w http.ResponseWriter, r *http.Request) {
	req, ok := s.readRequest(w, r)
	if !ok {
		return
	}
	if err := s.workflow.Dismiss(req.GroupID, req.ActorID, req.Key); err != nil {
		fail(w, 400, err)
		return
	}
	reply(w, 200, map[string]bool{"ok": true})
}

func (s *Server) Confirm(w http.ResponseWriter, r *http.Request) {
	req, ok := s.readRequest(w, r)
	if !ok {
		return
	}
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

func (s *Server) Complete(w http.ResponseWriter, r *http.Request) {
	req, ok := s.readRequest(w, r)
	if !ok {
		return
	}
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
