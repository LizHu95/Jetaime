package llm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestContextBudgetBoundaries(t *testing.T) {
	request := testRequest()
	bound := InputTokenUpperBound(request) + request.Options.NumPredict
	request.Options.NumCtx = bound
	if err := CheckContextBudget(request, bound); err != nil {
		t.Fatal("exact bound should fit", err)
	}
	if err := CheckContextBudget(request, bound-1); !errors.Is(err, ErrContextLimit) {
		t.Fatal("configured capacity was bypassed", err)
	}
	request.Options.NumCtx = bound - 1
	if err := CheckContextBudget(request, bound+100); !errors.Is(err, ErrContextLimit) {
		t.Fatal("lower per-request capacity ignored", err)
	}
	request = testRequest()
	request.Options.NumPredict = int(^uint(0) >> 1)
	if err := CheckContextBudget(request, 8192); !errors.Is(err, ErrContextLimit) {
		t.Fatal("large output reserve overflowed", err)
	}
}

func TestAdapterRejectsOverBudgetWithoutHTTP(t *testing.T) {
	t.Setenv("TEST_LLM_KEY", "test-credential")
	for _, provider := range []string{"ollama", "openai-compatible"} {
		t.Run(provider, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(http.StatusInternalServerError)
			}))
			defer server.Close()
			adapter, err := (Config{Provider: provider, BaseURL: server.URL, Model: "test", APIKeyEnv: "TEST_LLM_KEY", OutputMode: "json_schema", ContextTokens: 8192}).New(time.Second)
			if err != nil {
				t.Fatal(err)
			}
			for _, scenario := range []string{"messages", "schema", "output", "raised-context", "missing-context", "missing-output"} {
				t.Run(scenario, func(t *testing.T) {
					request := testRequest()
					switch scenario {
					case "messages":
						request.Messages[1].Content = strings.Repeat("中文", 2000)
					case "schema":
						request.Format = []byte(`{"description":"` + strings.Repeat("x", 8192) + `"}`)
					case "output":
						request.Options.NumPredict = 8192
					case "raised-context":
						request.Options.NumCtx = 32768
						request.Messages[1].Content = strings.Repeat("x", 8192)
					case "missing-context":
						request.Options.NumCtx = 0
					case "missing-output":
						request.Options.NumPredict = 0
					}
					result, err := adapter.Call(context.Background(), request)
					if !errors.Is(err, ErrContextLimit) || result.Content != "" || result.Raw != nil || calls.Load() != 0 {
						t.Fatalf("over-budget request reached provider: calls=%d err=%v", calls.Load(), err)
					}
				})
			}
		})
	}
}
