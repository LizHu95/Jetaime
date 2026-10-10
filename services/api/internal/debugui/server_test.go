package debugui

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/LizHu95/Jetaime/services/api/internal/decisions"
	"github.com/LizHu95/Jetaime/services/api/internal/demo"
)

func call(t *testing.T, s http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest(method, "http://localhost:8080/api/"+path, bytes.NewReader(data))
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func decode(t *testing.T, w *httptest.ResponseRecorder) response {
	t.Helper()
	var out response
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err, w.Body.String())
	}
	return out
}
func newServer(t *testing.T, c Config) *Server {
	t.Helper()
	s, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func generate(t *testing.T, s *Server, scenario, provider string) response {
	t.Helper()
	sc, err := demo.NewFixture().Scenario(scenario)
	if err != nil {
		t.Fatal(err)
	}
	w := call(t, s, "POST", "decisions", input{Request: sc.Request, Provider: provider})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	return decode(t, w)
}
func TestWorkflowAndTrace(t *testing.T) {
	s := newServer(t, Config{})
	first := generate(t, s, "normal", "mock")
	d := first.Decision
	if len(d.Result.Options) != 3 || first.Trace == nil {
		t.Fatal("missing recommendations or trace")
	}
	stages := map[string]bool{}
	for _, stage := range first.Trace.Stages {
		stages[stage.Name] = true
	}
	for _, name := range []string{"build_context", "evaluate_candidates", "validate_result", "commit"} {
		if !stages[name] {
			t.Fatalf("missing stage %s", name)
		}
	}
	feedback := input{ActorID: demo.UserB, EventID: "reject-1", Action: "reject", OptionID: d.Result.Options[0].OptionID}
	for i := 0; i < 2; i++ {
		w := call(t, s, "POST", "decisions/"+d.ID+"/feedback", feedback)
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
	}
	batch := input{ActorID: demo.UserB, EventID: "batch-1", DecisionID: d.ID}
	w := call(t, s, "POST", "sessions/"+d.SessionID+"/batch", batch)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	next := decode(t, w)
	for _, option := range next.Decision.Result.Options {
		if option.Selection.NoteID == d.Result.Options[0].Selection.NoteID {
			t.Fatal("rejected recommendation reappeared")
		}
	}
	repeated := decode(t, call(t, s, "POST", "sessions/"+d.SessionID+"/batch", batch))
	if repeated.Decision == nil || repeated.Decision.ID != next.Decision.ID {
		t.Fatal("batch retry duplicated result")
	}
	w = call(t, s, "GET", "sessions/"+d.SessionID+"/history", nil)
	var history []any
	_ = json.Unmarshal(w.Body.Bytes(), &history)
	if len(history) != 2 {
		t.Fatal(w.Body.String())
	}
	w = call(t, s, "GET", "traces/"+first.Trace.ID, nil)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
}
func TestTraceDownload(t *testing.T) {
	s := newServer(t, Config{})
	out := generate(t, s, "normal", "mock")
	w := call(t, s, "GET", "traces/"+out.Trace.ID+"/download", nil)
	if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Disposition"), "attachment;") {
		t.Fatal(w.Code, w.Header())
	}
	var trace Trace
	if err := json.Unmarshal(w.Body.Bytes(), &trace); err != nil || trace.ID != out.Trace.ID || len(trace.Stages) == 0 {
		t.Fatal("invalid trace download", err)
	}
}

func TestFixedScenarios(t *testing.T) {
	for _, scenario := range demo.NewFixture().Scenarios {
		t.Run(scenario.Name, func(t *testing.T) {
			out := generate(t, newServer(t, Config{}), scenario.Name, "mock")
			if out.Decision.Result.Outcome != scenario.Expected {
				t.Fatal(out.Decision.Result.Outcome)
			}
		})
	}
}
func fixtureFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.json")
	data, _ := json.Marshal(demo.NewFixture())
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func TestReloadIsTransactional(t *testing.T) {
	path := fixtureFile(t)
	s := newServer(t, Config{FixturePath: path})
	old := generate(t, s, "normal", "mock")
	if err := os.WriteFile(path, []byte("broken JSON"), 0600); err != nil {
		t.Fatal(err)
	}
	w := call(t, s, "POST", "fixture/reload", input{})
	if w.Code != 422 || decode(t, w).Trace.Error == "" {
		t.Fatal(w.Body.String())
	}
	current := decode(t, call(t, s, "GET", "current", nil))
	if current.Decision.ID != old.Decision.ID {
		t.Fatal("failed reload changed current")
	}
	data, _ := json.Marshal(demo.NewFixture())
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	w = call(t, s, "POST", "fixture/reload", input{})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	next := decode(t, w)
	if next.Decision.SessionID == old.Decision.SessionID {
		t.Fatal("reload reused session")
	}
	w = call(t, s, "POST", "sessions/"+old.Decision.SessionID+"/batch", input{ActorID: demo.UserA, EventID: "old", DecisionID: old.Decision.ID})
	if w.Code == 200 {
		t.Fatal("expired session remained writable")
	}
}
func TestOllamaFailureRetryAndSessionProvider(t *testing.T) {
	var requests atomic.Int32
	var healthy atomic.Bool
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if healthy.Load() {
			var request struct {
				Messages []struct {
					Content string `json:"content"`
				} `json:"messages"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
				return
			}
			var input decisions.Context
			if err := json.Unmarshal([]byte(strings.SplitN(request.Messages[1].Content, "\n", 2)[1]), &input); err != nil {
				t.Error(err)
				return
			}
			result, err := (demo.FixedProvider{}).Generate(r.Context(), input)
			if err != nil {
				t.Error(err)
				return
			}
			data, _ := json.Marshal(result)
			var output map[string]any
			_ = json.Unmarshal(data, &output)
			for _, raw := range output["options"].([]any) {
				delete(raw.(map[string]any), "optionId")
			}
			content, _ := json.Marshal(output)
			_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]any{"role": "assistant", "content": string(content)}, "done": true})
			return
		}
		_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"broken output"},"done":true}`))
	}))
	defer model.Close()
	s := newServer(t, Config{OllamaURL: model.URL})
	first := generate(t, s, "normal", "mock")
	sc, _ := demo.NewFixture().Scenario("normal")
	w := call(t, s, "POST", "decisions", input{Request: sc.Request, Provider: "ollama"})
	out := decode(t, w)
	if w.Code != 422 || out.Trace.Error == "" {
		t.Fatal(w.Body.String())
	}
	found := false
	for _, stage := range out.Trace.Stages {
		if stage.Name == "ollama.generate" {
			found = stage.Input != nil && stage.Output != nil
		}
	}
	if !found {
		t.Fatal("failed model input/output not captured")
	}
	current := decode(t, call(t, s, "GET", "current", nil))
	if current.Decision.ID != first.Decision.ID {
		t.Fatal("failed generation replaced current")
	}
	// 无候选的 Ollama 会话成功创建；先前 Mock 会话换批仍使用自己的配置。
	generate(t, s, "unknown", "ollama")
	w = call(t, s, "POST", "sessions/"+first.Decision.SessionID+"/batch", input{ActorID: demo.UserA, DecisionID: first.Decision.ID, EventID: "keep-mock"})
	if w.Code != 200 || requests.Load() != 1 || decode(t, w).Trace.Provider != "mock" {
		t.Fatal(w.Body.String(), requests.Load())
	}
	// 把当前会话绑定到故障模型，验证失败后原命令保留并可重试。
	s.bindings[first.Decision.SessionID] = sessionConfig{"ollama", s.model}
	latest := decode(t, w).Decision
	w = call(t, s, "POST", "sessions/"+latest.SessionID+"/batch", input{ActorID: demo.UserA, DecisionID: latest.ID, EventID: "failed-batch"})
	if w.Code != 422 {
		t.Fatal(w.Body.String())
	}
	pending := s.pending[latest.SessionID]
	if pending.EventID != "failed-batch" {
		t.Fatal("failed command lost")
	}
	w = call(t, s, "POST", "sessions/"+latest.SessionID+"/retry", input{ActorID: demo.UserB})
	if w.Code != 403 {
		t.Fatal("another actor retried command")
	}
	w = call(t, s, "POST", "sessions/"+latest.SessionID+"/retry", input{ActorID: demo.UserA})
	if w.Code != 422 || s.pending[latest.SessionID] != pending || requests.Load() != 3 {
		t.Fatal(w.Body.String(), requests.Load())
	}
	healthy.Store(true)
	w = call(t, s, "POST", "sessions/"+latest.SessionID+"/retry", input{ActorID: demo.UserA})
	if w.Code != 200 || requests.Load() != 4 {
		t.Fatal(w.Body.String(), requests.Load())
	}
	if _, ok := s.pending[latest.SessionID]; ok {
		t.Fatal("successful retry left pending command")
	}
	if next := decode(t, w).Decision; next == nil || next.ID == latest.ID {
		t.Fatal("retry failed to create next decision")
	}

}
func TestGuardsAndBusyReads(t *testing.T) {
	s := newServer(t, Config{})
	for _, test := range []struct{ host, origin string }{{"evil.example", ""}, {"localhost:8080", "https://evil.example"}} {
		r := httptest.NewRequest("GET", "http://localhost:8080/api/config", nil)
		r.Host = test.host
		r.Header.Set("Origin", test.origin)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal("untrusted host or origin accepted")
		}
	}
	s.opMu.Lock()
	if w := call(t, s, "GET", "config", nil); w.Code != 200 {
		t.Fatal("read blocked during generation")
	}
	if w := call(t, s, "POST", "decisions", input{}); w.Code != 409 {
		t.Fatal("parallel mutation accepted")
	}
	s.opMu.Unlock()
	r := httptest.NewRequest("POST", "http://localhost:8080/api/decisions", strings.NewReader(`{"unknown":1}`))
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatal(w.Body.String())
	}
	w = call(t, s, "POST", "not-found", input{})
	if w.Code != 404 {
		t.Fatal(w.Body.String())
	}
}
func TestTraceRetention(t *testing.T) {
	s := newServer(t, Config{})
	first := generate(t, s, "empty", "mock")
	for i := 0; i < 100; i++ {
		generate(t, s, "empty", "mock")
	}
	if w := call(t, s, "GET", "traces/"+first.Trace.ID, nil); w.Code != 404 {
		t.Fatal("old trace retained")
	}
	if len(s.traces) != 100 {
		t.Fatal(len(s.traces))
	}
}

func TestConcurrentReadsAndCancellation(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-release }))
	defer model.Close()
	s := newServer(t, Config{OllamaURL: model.URL})
	first := generate(t, s, "normal", "mock")
	sc, _ := demo.NewFixture().Scenario("normal")
	body, _ := json.Marshal(input{Request: sc.Request, Provider: "ollama"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest("POST", "http://localhost:8080/api/decisions", bytes.NewReader(body)).WithContext(ctx)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { w := httptest.NewRecorder(); s.ServeHTTP(w, req); done <- w }()
	<-started
	for _, path := range []string{"config", "current", "history", "traces/" + first.Trace.ID} {
		if w := call(t, s, "GET", path, nil); w.Code != 200 {
			t.Fatal(w.Body.String())
		}
	}
	if w := call(t, s, "POST", "fixture/reload", input{}); w.Code != 409 {
		t.Fatal("mutation did not report busy")
	}
	cancel()
	close(release)
	w := <-done
	if w.Code == 200 || decode(t, w).Trace.Error == "" {
		t.Fatal("cancelled operation committed")
	}
	if current := decode(t, call(t, s, "GET", "current", nil)); current.Decision.ID != first.Decision.ID {
		t.Fatal("cancellation changed current")
	}
}
