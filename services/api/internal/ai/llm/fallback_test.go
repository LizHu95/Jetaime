package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func fallbackServer(t *testing.T, model string, calls *atomic.Int32, status int, slow bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Model != model {
			t.Errorf("model = %q, want %q", body.Model, model)
		}
		if slow {
			<-r.Context().Done()
			return
		}
		if status != 200 {
			w.WriteHeader(status)
			_, _ = w.Write([]byte("secret-provider-error"))
			return
		}
		_, _ = w.Write([]byte(`{"done":true,"done_reason":"stop","message":{"role":"assistant","content":"{}"}}`))
	}))
}

func fallbackAdapter(t *testing.T, servers []*httptest.Server, timeout time.Duration) *Adapter {
	t.Helper()
	c := Config{Provider: "ollama", BaseURL: servers[0].URL, Model: "test", ContextTokens: 8192}
	for _, s := range servers[1:] {
		c.Fallbacks = append(c.Fallbacks, Config{Provider: "ollama", BaseURL: s.URL, Model: "backup", ContextTokens: 4096})
	}
	a, err := c.New(timeout)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestFallbackSwitchesAndSticksConcurrently(t *testing.T) {
	var first, second, third atomic.Int32
	s1 := fallbackServer(t, "test", &first, 503, false)
	defer s1.Close()
	s2 := fallbackServer(t, "backup", &second, 200, false)
	defer s2.Close()
	s3 := fallbackServer(t, "backup", &third, 200, false)
	defer s3.Close()
	a := fallbackAdapter(t, []*httptest.Server{s1, s2, s3}, time.Second)
	if out, err := a.Call(context.Background(), testRequest()); err != nil || out.Content != "{}" {
		t.Fatalf("fallback: %+v %v", out, err)
	}
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			if _, err := a.Call(context.Background(), testRequest()); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if first.Load() != 1 || second.Load() != 11 || third.Load() != 0 {
		t.Fatalf("calls: %d %d %d", first.Load(), second.Load(), third.Load())
	}
	s2.Close()
	if _, err := a.Call(context.Background(), testRequest()); err != nil {
		t.Fatal(err)
	}
	if third.Load() != 1 {
		t.Fatal("did not switch away from failed active candidate")
	}
}

func TestFallbackTimeoutCancellationAndValidation(t *testing.T) {
	var first, second atomic.Int32
	s1 := fallbackServer(t, "test", &first, 200, true)
	defer s1.Close()
	s2 := fallbackServer(t, "backup", &second, 200, false)
	defer s2.Close()
	a := fallbackAdapter(t, []*httptest.Server{s1, s2}, 300*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.Call(ctx, testRequest()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	for _, in := range []Request{{Model: "wrong"}, {Model: "test", Stream: true}, {Model: "test", Format: json.RawMessage(`invalid`)}} {
		if _, err := a.Call(context.Background(), in); err == nil {
			t.Fatal("invalid request accepted")
		}
	}
	if first.Load() != 0 || second.Load() != 0 {
		t.Fatal("invalid request performed I/O")
	}
	if _, err := a.Call(context.Background(), testRequest()); err != nil {
		t.Fatal(err)
	}
	if first.Load() != 1 || second.Load() != 1 {
		t.Fatal("timeout did not trigger fallback")
	}
}

func TestFallbackExhaustionAndContextBudget(t *testing.T) {
	var first, second atomic.Int32
	s1 := fallbackServer(t, "test", &first, 503, false)
	defer s1.Close()
	s2 := fallbackServer(t, "backup", &second, 429, false)
	defer s2.Close()
	a := fallbackAdapter(t, []*httptest.Server{s1, s2}, time.Second)
	out, err := a.Call(context.Background(), testRequest())
	if err == nil || !strings.Contains(err.Error(), "all model candidates failed") || strings.Contains(err.Error(), "secret") || out.Raw != nil {
		t.Fatalf("exhaustion: %+v %v", out, err)
	}
	req := testRequest()
	req.Options.NumPredict = 9000
	if _, err := a.Call(context.Background(), req); !errors.Is(err, ErrContextLimit) {
		t.Fatalf("budget: %v", err)
	}
	if first.Load() != 1 || second.Load() != 1 {
		t.Fatal("oversized request performed I/O")
	}
	a.active.Store(1)
	req = testRequest()
	req.Messages[0].Content = strings.Repeat("x", 4500)
	if _, err := a.Call(context.Background(), req); err == nil {
		t.Fatal("failed primary accepted")
	}
	if first.Load() != 2 || second.Load() != 1 {
		t.Fatal("candidate capacity ignored")
	}
}

func TestFallbackRespectsCallerDeadline(t *testing.T) {
	var first, second atomic.Int32
	s1 := fallbackServer(t, "test", &first, 200, true)
	defer s1.Close()
	s2 := fallbackServer(t, "backup", &second, 200, false)
	defer s2.Close()
	a := fallbackAdapter(t, []*httptest.Server{s1, s2}, time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := a.Call(ctx, testRequest()); err != nil {
		t.Fatal(err)
	}
	if first.Load() != 1 || second.Load() != 1 {
		t.Fatal("caller deadline left no time for fallback")
	}
}

func TestFallbackHealthyPrimaryAndInflightCancellation(t *testing.T) {
	var first, second atomic.Int32
	s1 := fallbackServer(t, "test", &first, 200, false)
	defer s1.Close()
	s2 := fallbackServer(t, "backup", &second, 200, false)
	defer s2.Close()
	a := fallbackAdapter(t, []*httptest.Server{s1, s2}, time.Second)
	if _, err := a.Call(context.Background(), testRequest()); err != nil {
		t.Fatal(err)
	}
	if first.Load() != 1 || second.Load() != 0 {
		t.Fatal("healthy primary used fallback")
	}
	started := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(new(any)); err != nil {
			t.Error(err)
		}
		close(started)
		<-r.Context().Done()
	}))
	defer slow.Close()
	a = fallbackAdapter(t, []*httptest.Server{slow, s2}, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { <-started; cancel() }()
	if _, err := a.Call(ctx, testRequest()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	if second.Load() != 0 {
		t.Fatal("canceled request tried fallback")
	}
}
