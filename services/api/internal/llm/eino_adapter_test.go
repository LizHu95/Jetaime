package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var testSchema = json.RawMessage(`{"type":"object","additionalProperties":false,"required":["answer"],"properties":{"answer":{"type":["string","null"],"minLength":1}}}`)

func testRequest() Request {
	return Request{Model: "test", Messages: []Message{{Role: "system", Content: "Return JSON"}, {Role: "user", Content: "Test"}}, Format: testSchema, Options: Options{NumCtx: 8192, NumPredict: 123, Temperature: 0}}
}

func TestEinoWireMapping(t *testing.T) {
	for _, provider := range []string{"ollama", "openai-compatible"} {
		for _, mode := range []string{"json_schema", "json_object"} {
			t.Run(provider+"/"+mode, func(t *testing.T) {
				t.Setenv("TEST_LLM_KEY", "test-credential")
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body map[string]json.RawMessage
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if string(body["model"]) != `"test"` || string(body["stream"]) == "true" {
						t.Error("wrong model or stream")
					}
					if provider == "ollama" {
						if r.URL.Path != "/api/chat" || string(body["think"]) != "false" {
							t.Error("wrong Ollama request")
						}
						var got, want any
						_ = json.Unmarshal(body["format"], &got)
						_ = json.Unmarshal(testSchema, &want)
						b1, _ := json.Marshal(got)
						b2, _ := json.Marshal(want)
						if string(b1) != string(b2) {
							t.Error("schema changed")
						}
						var options map[string]any
						_ = json.Unmarshal(body["options"], &options)
						if options["num_ctx"] != float64(8192) || options["num_predict"] != float64(123) {
							t.Error("Ollama options lost")
						}
						_, _ = w.Write([]byte(`{"done":true,"done_reason":"stop","message":{"role":"assistant","content":"{}"},"prompt_eval_count":10,"eval_count":2}`))
					} else {
						if r.URL.Path != "/compatible-mode/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer test-credential" {
							t.Error("wrong cloud endpoint/auth")
						}
						if body["options"] != nil || body["format"] != nil || body["think"] != nil || string(body["max_tokens"]) != "123" || string(body["enable_thinking"]) != "false" || string(body["temperature"]) != "0" {
							t.Error("wrong cloud parameters")
						}
						var format struct {
							Type       string `json:"type"`
							JSONSchema struct {
								Schema json.RawMessage `json:"schema"`
								Strict bool            `json:"strict"`
							} `json:"json_schema"`
						}
						_ = json.Unmarshal(body["response_format"], &format)
						if format.Type != mode {
							t.Errorf("wrong output mode %s", format.Type)
						}
						if mode == "json_schema" && (!format.JSONSchema.Strict || !strings.Contains(string(format.JSONSchema.Schema), `["string","null"]`)) {
							t.Error("schema constraints lost")
						}
						_, _ = w.Write([]byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"{}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`))
					}
				}))
				defer server.Close()
				base := server.URL
				if provider != "ollama" {
					base += "/compatible-mode/v1"
				}
				thinking := false
				a, err := (Config{Provider: provider, BaseURL: base, Model: "test", APIKeyEnv: "TEST_LLM_KEY", OutputMode: mode, EnableThinking: &thinking, ContextTokens: 8192}).New(time.Second)
				if err != nil {
					t.Fatal(err)
				}
				out, err := a.Call(context.Background(), testRequest())
				if err != nil {
					t.Fatal(err)
				}
				if out.Content != "{}" || out.Raw == nil || out.PromptTokens == nil || *out.PromptTokens != 10 || out.CompletionTokens == nil || *out.CompletionTokens != 2 {
					t.Fatalf("bad normalized response: %+v", out)
				}
			})
		}
	}
}

func TestEinoCloudFailuresAndPinnedModel(t *testing.T) {
	t.Setenv("TEST_LLM_KEY", "test-sensitive-key")
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"truncated", `{"choices":[{"message":{"content":"{}"},"finish_reason":"length"}]}`, 200},
		{"empty", `{"choices":[{"message":{"content":""},"finish_reason":"stop"}]}`, 200},
		{"missing-choice", `{"choices":[]}`, 200},
		{"missing-finish", `{"choices":[{"message":{"content":"{}"}}]}`, 200},
		{"invalid", "broken", 200},
		{"auth", `{"error":{"message":"test-sensitive-key"}}`, 401},
		{"oversized", strings.Repeat("x", (1<<20)+1), 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer s.Close()
			a, err := (Config{Provider: "openai-compatible", BaseURL: s.URL + "/v1", Model: "test", APIKeyEnv: "TEST_LLM_KEY", OutputMode: "json_schema", ContextTokens: 8192}).New(time.Second)
			if err != nil {
				t.Fatal(err)
			}
			out, err := a.Call(context.Background(), testRequest())
			if err == nil {
				t.Fatal("failure accepted")
			}
			encoded, _ := json.Marshal(out)
			if strings.Contains(string(encoded)+err.Error(), "test-sensitive-key") {
				t.Fatal("credential leaked")
			}
			if calls != 1 {
				t.Fatalf("unexpected retry: %d", calls)
			}
			req := testRequest()
			req.Model = "other"
			if _, err := a.Call(context.Background(), req); err == nil || calls != 1 {
				t.Fatal("startup model overridden")
			}
			if _, err := a.ReviewCacheIdentity(context.Background(), "test"); err == nil {
				t.Fatal("mutable cloud alias cached")
			}
		})
	}
}

func TestEinoCancellation(t *testing.T) {
	release := make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	defer s.Close()
	defer close(release)
	a, err := (Config{Provider: "ollama", BaseURL: s.URL, Model: "test", ContextTokens: 8192}).New(time.Millisecond * 20)
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Call(context.Background(), testRequest())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline lost: %v", err)
	}
}

func TestEinoOllamaRejectsIncompleteAndTruncatedResponses(t *testing.T) {
	for _, body := range []string{
		`{"done":false,"message":{"role":"assistant","content":"{}"}}`,
		`{"done":true,"done_reason":"length","message":{"role":"assistant","content":"{}"}}`,
		`{"done":true,"error":"model error","message":{"role":"assistant","content":"{}"}}`,
	} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
		a, err := (Config{Provider: "ollama", BaseURL: s.URL, Model: "test", ContextTokens: 8192}).New(time.Second)
		if err != nil {
			s.Close()
			t.Fatal(err)
		}
		_, err = a.Call(context.Background(), testRequest())
		s.Close()
		if err == nil {
			t.Fatalf("incomplete/truncated response accepted: %s", body)
		}
	}
}

func TestEinoOllamaCacheIdentityTracksDigestAndEndpoint(t *testing.T) {
	digest := "first"
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" || r.Method != http.MethodGet {
			t.Error("wrong fingerprint endpoint")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"models": []map[string]string{{"name": "test:latest", "digest": digest}}})
	}))
	defer s.Close()
	c := Config{Provider: "ollama", BaseURL: s.URL, Model: "test", ContextTokens: 8192}
	a, err := c.New(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	first, err := a.ReviewCacheIdentity(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(first, s.URL+"|") {
		t.Fatal("endpoint missing from fingerprint")
	}
	digest = "second"
	second, err := a.ReviewCacheIdentity(context.Background(), "test")
	if err != nil || first == second {
		t.Fatal("changed weights reused cache identity")
	}
	digest = ""
	if identity, err := a.ReviewCacheIdentity(context.Background(), "test"); err == nil || identity != "" {
		t.Fatal("missing digest accepted")
	}
}
