// Package debugui 提供仅限本机的开发调试 API 与静态页面。
package debugui

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/decisions"
	"github.com/LizHu95/Jetaime/services/api/internal/demo"
	"github.com/LizHu95/Jetaime/services/api/internal/ollama"
	"github.com/LizHu95/Jetaime/services/api/internal/telemetry"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

//go:embed web/*
var assets embed.FS

type Config struct {
	FixturePath   string
	OllamaURL     string
	Model         string
	ModelTimeout  time.Duration
	ContextTokens int
	Trace         telemetry.Config // Phoenix 上报可选；页面记录始终保留。
}
type batchCommand struct{ DecisionID, ActorID, EventID string }
type sessionConfig struct{ Provider, Model string }
type entry struct{ ID, SessionID, RequesterID, TraceID string }
type Server struct {
	config              Config
	opMu                sync.Mutex // 变更串行化；读取仍可在模型运行期间返回。
	mu                  sync.RWMutex
	fixture             demo.Fixture
	store               *decisions.MemoryStore
	service             *decisions.DecisionService
	providerName, model string
	current             *decisions.Decision
	pending             map[string]batchCommand
	bindings            map[string]sessionConfig
	history             []entry
	traces              map[string]Trace
	traceOrder          []string
	handler             http.Handler
	tracerProvider      *sdktrace.TracerProvider
}
type input struct {
	Request    decisions.Request        `json:"request"`
	Provider   string                   `json:"provider"`
	Model      string                   `json:"model"`
	ActorID    string                   `json:"actorId"`
	DecisionID string                   `json:"decisionId"`
	EventID    string                   `json:"eventId"`
	Action     decisions.FeedbackAction `json:"action"`
	OptionID   string                   `json:"optionId"`
}
type response struct {
	Decision *decisions.Decision `json:"decision,omitempty"`
	Session  *decisions.Session  `json:"session,omitempty"`
	Trace    *Trace              `json:"trace,omitempty"`
	Error    string              `json:"error,omitempty"`
}

func New(config Config) (*Server, error) {
	if config.OllamaURL == "" {
		config.OllamaURL = "http://localhost:11434"
	}
	if config.Model == "" {
		config.Model = "qwen3.5:9b"
	}
	if config.ModelTimeout == 0 {
		config.ModelTimeout = 3 * time.Minute
	}
	if config.ContextTokens == 0 {
		config.ContextTokens = 8192
	}
	fixture := demo.NewFixture()
	var err error
	if config.FixturePath != "" {
		fixture, err = loadFixture(config.FixturePath)
		if err != nil {
			return nil, err
		}
	}
	store, err := decisions.NewMemoryStore(fixture.Dataset)
	if err != nil {
		return nil, err
	}
	provider, err := ollama.NewProvider(ollama.Config{BaseURL: config.OllamaURL, Model: config.Model, Timeout: config.ModelTimeout, ContextTokens: config.ContextTokens})
	if err != nil {
		return nil, err
	}
	service, err := newService(store, fixture, provider)
	if err != nil {
		return nil, err
	}
	s := &Server{config: config, fixture: fixture, store: store, service: service, providerName: "ollama", model: config.Model, pending: map[string]batchCommand{}, bindings: map[string]sessionConfig{}, traces: map[string]Trace{}, history: []entry{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/", s.api)
	files, err := fs.Sub(assets, "web")
	if err != nil {
		return nil, err
	}
	mux.Handle("/", http.FileServer(http.FS(files)))
	s.handler = s.localOnly(mux)
	s.tracerProvider, err = telemetry.NewProvider(context.Background(), config.Trace, sdktrace.WithSpanProcessor(&recordingRouter{}))
	if err != nil {
		return nil, err
	}
	return s, nil
}

// Shutdown 在 HTTP 请求结束后关闭共享 exporter，导出剩余追踪。
func (s *Server) Shutdown(ctx context.Context) error { return s.tracerProvider.Shutdown(ctx) }

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

// 本机工具含完整调试数据；拒绝非本机 Host 与跨源请求。
func (s *Server) localOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		ip := net.ParseIP(strings.Trim(host, "[]"))
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			http.Error(w, "local host required", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			if err != nil || u.Host != r.Host || u.Scheme != "http" {
				http.Error(w, "same origin required", http.StatusForbidden)
				return
			}
		}
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			http.Error(w, "same origin required", http.StatusForbidden)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}
func write(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func (s *Server) api(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.read(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		write(w, 405, response{Error: "method not allowed"})
		return
	}
	if !s.opMu.TryLock() {
		write(w, 409, response{Error: "另一个操作正在执行，请等待完成"})
		return
	}
	defer s.opMu.Unlock()
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var in input
	if err := decoder.Decode(&in); err != nil {
		write(w, 400, response{Error: "无效请求：" + err.Error()})
		return
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		write(w, 400, response{Error: "请求必须只包含一个 JSON 对象"})
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/")
	parts := strings.Split(path, "/")
	known := path == "decisions" || path == "fixture/reload" || (len(parts) == 3 && ((parts[0] == "decisions" && parts[2] == "feedback") || (parts[0] == "sessions" && (parts[2] == "batch" || parts[2] == "retry"))))
	if !known {
		write(w, 404, response{Error: "unknown API route"})
		return
	}
	ctx, rec := record(r.Context(), s.tracerProvider)
	trace := Trace{ID: uuid.NewString(), Operation: path, Provider: s.providerName, Model: s.model, StartedAt: time.Now()}
	if len(parts) == 3 && parts[0] == "sessions" {
		if binding, ok := s.bindings[parts[1]]; ok {
			trace.Provider, trace.Model = binding.Provider, binding.Model
		}
	}
	traceDecisionID := in.DecisionID
	if len(parts) == 3 && parts[0] == "decisions" {
		traceDecisionID = parts[1]
	}
	if traceDecisionID != "" {
		if d, err := s.service.GetDecision(traceDecisionID, in.ActorID); err == nil {
			if binding, ok := s.bindings[d.SessionID]; ok {
				trace.Provider, trace.Model = binding.Provider, binding.Model
			}
		}
	}
	ctx, span := telemetry.Start(ctx, "debug."+path, "CHAIN", in)
	trace.OTelTraceID = span.SpanContext().TraceID().String()
	span.SetAttributes(attribute.String("debug.trace_id", trace.ID))
	out, err := s.mutate(ctx, path, parts, in)
	telemetry.Output(span, out)
	telemetry.Finish(span, err)
	trace.DurationMS = float64(time.Since(trace.StartedAt)) / float64(time.Millisecond)
	trace.Stages = rec.snapshot()
	if path == "decisions" {
		trace.Provider = in.Provider
		if trace.Provider == "" {
			trace.Provider = "ollama"
		}
		trace.Model = in.Model
		if trace.Model == "" {
			trace.Model = s.config.Model
		}
	}
	if err != nil {
		trace.Error = err.Error()
		out.Error = err.Error()
	}
	s.mu.Lock()
	s.traces[trace.ID] = trace
	s.traceOrder = append(s.traceOrder, trace.ID)
	if len(s.traceOrder) > 100 {
		delete(s.traces, s.traceOrder[0])
		s.traceOrder = s.traceOrder[1:]
	}
	if err == nil && out.Decision != nil {
		d := out.Decision
		found := false
		for _, e := range s.history {
			if e.ID == d.ID {
				found = true
				break
			}
		}
		if !found {
			s.history = append(s.history, entry{d.ID, d.SessionID, d.RequesterID, trace.ID})
		}
	}
	s.mu.Unlock()
	out.Trace = &trace
	status := 200
	if err != nil {
		status = 422
		if errors.Is(err, decisions.ErrForbidden) {
			status = 403
		}
		if errors.Is(err, decisions.ErrNotFound) {
			status = 404
		}
		if errors.Is(err, decisions.ErrStateChanged) || errors.Is(err, decisions.ErrBusy) {
			status = 409
		}
	}
	write(w, status, out)
}

func (s *Server) mutate(ctx context.Context, path string, parts []string, in input) (response, error) {
	switch {
	case path == "decisions":
		name := in.Provider
		if name == "" {
			name = "ollama"
		}
		model := in.Model
		if model == "" {
			model = s.config.Model
		}
		p, err := s.makeProvider(name, model)
		if err != nil {
			return response{}, err
		}
		service, err := newService(s.store, s.fixture, p)
		if err != nil {
			return response{}, err
		}
		d, err := service.Generate(ctx, in.Request, in.Request.RequesterID)
		if err != nil {
			return response{}, err
		}
		s.mu.Lock()
		s.service = service
		s.providerName = name
		s.model = model
		s.current = &d
		s.bindings[d.SessionID] = sessionConfig{name, model}
		s.mu.Unlock()
		return result(service, d, in.Request.RequesterID)
	case path == "fixture/reload":
		if s.config.FixturePath == "" {
			return response{}, fmt.Errorf("启动时需要指定 -fixture 文件才能重载")
		}
		if s.current == nil {
			return response{}, fmt.Errorf("请先生成一轮推荐")
		}
		old := s.current
		binding := sessionConfig{s.providerName, s.model}
		if in.DecisionID != "" {
			d, err := s.service.GetDecision(in.DecisionID, in.ActorID)
			if err != nil {
				return response{}, err
			}
			old = &d
			var ok bool
			binding, ok = s.bindings[d.SessionID]
			if !ok {
				return response{}, decisions.ErrStateChanged
			}
		}
		f, err := loadFixture(s.config.FixturePath)
		if err != nil {
			return response{}, err
		}
		store, err := s.store.WithDataset(f.Dataset, time.Now())
		if err != nil {
			return response{}, err
		}
		p, err := s.makeProvider(binding.Provider, binding.Model)
		if err != nil {
			return response{}, err
		}
		service, err := newService(store, f, p)
		if err != nil {
			return response{}, err
		}
		req := decisions.Request{SpaceID: old.SpaceID, RequesterID: old.RequesterID, Task: old.Task, Query: old.Query, Conditions: old.Conditions}
		d, err := service.Generate(ctx, req, req.RequesterID)
		if err != nil {
			return response{}, err
		}
		s.mu.Lock()
		s.store = store
		s.service = service
		s.fixture = f
		s.current = &d
		s.providerName, s.model = binding.Provider, binding.Model
		s.pending = map[string]batchCommand{}
		s.bindings[d.SessionID] = binding
		s.mu.Unlock()
		return result(service, d, req.RequesterID)
	case parts[0] == "decisions":
		if in.EventID == "" {
			return response{}, fmt.Errorf("eventId is required")
		}
		session, err := s.service.Feedback(ctx, decisions.Feedback{ID: in.EventID, DecisionID: parts[1], Action: in.Action, OptionID: in.OptionID}, in.ActorID)
		if err != nil {
			return response{}, err
		}
		d, err := s.service.GetDecision(parts[1], in.ActorID)
		if err != nil {
			return response{}, err
		}
		return response{Decision: &d, Session: &session}, nil
	default:
		sessionID := parts[1]
		if _, err := s.service.GetSession(sessionID, in.ActorID); err != nil {
			return response{}, err
		}
		cmd := batchCommand{in.DecisionID, in.ActorID, in.EventID}
		if parts[2] == "retry" {
			var ok bool
			cmd, ok = s.pending[sessionID]
			if !ok {
				return response{}, fmt.Errorf("没有可重试的失败换批")
			}
			if cmd.ActorID != in.ActorID {
				return response{}, decisions.ErrForbidden
			}
		} else {
			d, err := s.service.GetDecision(cmd.DecisionID, cmd.ActorID)
			if err != nil {
				return response{}, err
			}
			if d.SessionID != sessionID || cmd.EventID == "" {
				return response{}, fmt.Errorf("invalid session or eventId")
			}
		}
		binding, ok := s.bindings[sessionID]
		if !ok {
			return response{}, decisions.ErrStateChanged
		}
		p, err := s.makeProvider(binding.Provider, binding.Model)
		if err != nil {
			return response{}, err
		}
		service, err := newService(s.store, s.fixture, p)
		if err != nil {
			return response{}, err
		}
		d, err := service.ChangeBatch(ctx, cmd.DecisionID, cmd.ActorID, cmd.EventID)
		s.mu.Lock()
		if err != nil {
			s.pending[sessionID] = cmd
		} else {
			delete(s.pending, sessionID)
			s.current = &d
			s.service = service
			s.providerName, s.model = binding.Provider, binding.Model
		}
		s.mu.Unlock()
		if err != nil {
			return response{}, err
		}
		return result(service, d, cmd.ActorID)
	}
}
func result(service *decisions.DecisionService, d decisions.Decision, actor string) (response, error) {
	session, err := service.GetSession(d.SessionID, actor)
	return response{Decision: &d, Session: &session}, err
}
func (s *Server) makeProvider(name, model string) (decisions.Provider, error) {
	switch name {
	case "ollama":
		return ollama.NewProvider(ollama.Config{BaseURL: s.config.OllamaURL, Model: model, Timeout: s.config.ModelTimeout, ContextTokens: s.config.ContextTokens})
	default:
		return nil, fmt.Errorf("provider must be ollama")
	}
}
func (s *Server) read(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	service := s.service
	fixture := s.fixture
	current := s.current
	history := append([]entry{}, s.history...)
	name, model := s.providerName, s.model
	pending := map[string]string{}
	for id, cmd := range s.pending {
		pending[id] = cmd.ActorID
	}
	s.mu.RUnlock()
	path := strings.TrimPrefix(r.URL.Path, "/api/")
	parts := strings.Split(path, "/")
	actor := r.URL.Query().Get("actorId")
	if actor == "" {
		actor = demo.UserA
	}
	switch {
	case path == "config":
		write(w, 200, map[string]any{"provider": name, "providers": []string{"ollama"}, "model": model, "ollamaUrl": s.config.OllamaURL, "modelTimeout": s.config.ModelTimeout.String(), "contextTokens": s.config.ContextTokens, "fixturePath": s.config.FixturePath, "spaces": fixture.Dataset.Spaces, "members": fixture.Dataset.Members, "traceLimit": 100, "pending": pending})
		return
	case path == "scenarios":
		write(w, 200, fixture.Scenarios)
		return
	case path == "current":
		if current == nil {
			write(w, 200, response{})
			return
		}
		if r.URL.Query().Get("actorId") == "" {
			actor = current.RequesterID
		}
		d, err := service.GetDecision(current.ID, actor)
		if err != nil {
			write(w, 403, response{Error: err.Error()})
			return
		}
		out, err := result(service, d, actor)
		if err != nil {
			write(w, 403, response{Error: err.Error()})
			return
		}
		write(w, 200, out)
		return
	case path == "history" || (len(parts) == 3 && parts[0] == "sessions" && parts[2] == "history"):
		items := []map[string]any{}
		for _, e := range history {
			if len(parts) == 3 && e.SessionID != parts[1] {
				continue
			}
			d, err := service.GetDecision(e.ID, actor)
			if err != nil {
				continue
			}
			session, err := service.GetSession(e.SessionID, actor)
			if err != nil {
				continue
			}
			items = append(items, map[string]any{"decision": d, "session": session, "traceId": e.TraceID, "expired": !time.Now().Before(session.ExpiresAt)})
		}
		write(w, 200, items)
		return
	case parts[0] == "traces" && (len(parts) == 2 || (len(parts) == 3 && parts[2] == "download")):
		s.mu.RLock()
		trace, ok := s.traces[parts[1]]
		s.mu.RUnlock()
		if !ok {
			write(w, 404, response{Error: "Trace 已过期或不存在（仅保留最近 100 次操作）"})
			return
		}
		if len(parts) == 3 {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"jetaime-trace-%s.json\"", trace.ID))
			encoder := json.NewEncoder(w)
			encoder.SetIndent("", "  ")
			_ = encoder.Encode(trace)
			return
		}
		write(w, 200, trace)
		return
	default:
		write(w, 404, response{Error: "unknown API route"})
	}
}
func loadFixture(path string) (demo.Fixture, error) {
	file, err := os.Open(path)
	if err != nil {
		return demo.Fixture{}, err
	}
	defer func() { _ = file.Close() }()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var f demo.Fixture
	if err := decoder.Decode(&f); err != nil {
		return f, err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return f, fmt.Errorf("fixture must contain one JSON object")
	}
	return f, nil
}
func newService(store *decisions.MemoryStore, f demo.Fixture, p decisions.Provider) (*decisions.DecisionService, error) {
	data, err := json.Marshal(f)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(data)
	return decisions.NewDecisionService(store, observedChecker{Facts: f.Facts}, p, decisions.ServiceConfig{DataVersion: hex.EncodeToString(hash[:])})
}

// 只记录合法候选的人工事实，便于核对检查结论，不把整个资料集放入 Trace。
type observedChecker struct{ Facts map[string]demo.Fact }

func (c observedChecker) Evaluate(ctx context.Context, input decisions.Context) (decisions.SelectEvaluation, error) {
	facts := map[string]demo.Fact{}
	for _, n := range input.Candidates {
		if f, ok := c.Facts[n.ID]; ok {
			facts[n.ID] = f
		}
	}
	return telemetry.Step(ctx, "check_facts", map[string]any{"conditions": input.Conditions, "participants": input.Participants, "facts": facts}, func(ctx context.Context) (decisions.SelectEvaluation, error) {
		return (demo.Checker{Facts: c.Facts}).Evaluate(ctx, input)
	})
}
