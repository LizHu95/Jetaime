package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestOllamaProtocolAndFailureTrace(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		failed     bool
	}{{"success", `{"done":true,"done_reason":"stop","message":{"role":"assistant","content":"{}"},"prompt_eval_count":10}`, 200, false}, {"truncated", `{"done":true,"done_reason":"length","message":{"content":"{}"}}`, 200, true}, {"incomplete", `{"done":false,"message":{"content":"{}"}}`, 200, true}, {"missing", `{"error":"model not found"}`, 404, true}} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/chat" || r.Method != "POST" {
					t.Error("wrong endpoint")
				}
				var request ModelRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				if request.Stream || request.Think || !json.Valid(request.Format) {
					t.Error("wrong structured request")
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			model, err := NewOllama(server.URL, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			result, err := model.Call(context.Background(), ModelRequest{Model: "test", Format: extractSchema})
			if (err != nil) != tc.failed {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.Raw == nil {
				t.Fatal("raw response lost")
			}
		})
	}
}
