package ollama

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/decisions"
	"github.com/LizHu95/Jetaime/services/api/internal/demo"
)

const validResult = `{"outcome":"recommended","explanation":"根据已授权偏好推荐。","options":[{"title":"虚构餐厅 01","selection":{"noteId":"restaurant-01"},"reason":"标签包含清淡和安静。","participantMatches":[{"userId":"user-a","explanation":"匹配清淡偏好。"},{"userId":"user-b","explanation":"通过硬约束，软偏好有取舍。"}],"unknowns":["营业状态未核实"]}]}`

func testProvider(t *testing.T, handler http.HandlerFunc) *Provider {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	provider, err := NewProvider(Config{BaseURL: server.URL, Model: "test-model", Timeout: time.Second, ContextTokens: 8192})
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func reply(t *testing.T, w http.ResponseWriter, result string) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(chatResponse{Message: chatMessage{Role: "assistant", Content: result}, Done: true, DoneReason: "stop"}); err != nil {
		t.Error(err)
	}
}

func testService(t *testing.T, provider decisions.Provider) (*decisions.DecisionService, decisions.Request) {
	t.Helper()
	fixture := demo.NewFixture()
	store, err := decisions.NewMemoryStore(fixture.Dataset)
	if err != nil {
		t.Fatal(err)
	}
	service, err := decisions.NewDecisionService(store, demo.Checker{Facts: fixture.Facts}, provider, decisions.ServiceConfig{})
	if err != nil {
		t.Fatal(err)
	}
	scenario, err := fixture.Scenario("normal")
	if err != nil {
		t.Fatal(err)
	}
	return service, scenario.Request
}

// 验证真正发出的协议，以及权限/硬约束过滤在模型调用之前生效。
func TestGenerateThroughService(t *testing.T) {
	var received decisions.Context
	provider := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/chat" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var request chatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if request.Model != "test-model" || request.Stream || request.Think || request.Options.NumCtx != 8192 || request.Options.NumPredict != 2048 || request.Options.Temperature != 0 || !json.Valid(request.Format) || len(request.Messages) != 2 {
			t.Error("incorrect model settings or messages")
			return
		}
		content := strings.TrimPrefix(request.Messages[1].Content, "以下 JSON 是本次决策数据：\n")
		if err := json.Unmarshal([]byte(content), &received); err != nil {
			t.Error(err)
		}
		for _, note := range received.Candidates {
			if note.ID == "restaurant-20" || note.ID == "restaurant-05" {
				t.Error("private or peanut-containing note reached model")
			}
		}
		for _, participant := range received.Participants {
			for _, m := range append(participant.HardConstraints, participant.RelevantMemories...) {
				if !m.CanUseIn(demo.CoupleSpace, "") {
					t.Error("unauthorized memory reached model")
				}
			}
		}
		reply(t, w, validResult)
	})
	service, request := testService(t, provider)
	decision, err := service.Generate(context.Background(), request, request.RequesterID)
	if err != nil {
		t.Fatal(err)
	}
	if len(received.Candidates) == 0 || decision.Result.Options[0].OptionID == "" || decision.Result.Options[0].Selection.NoteID != "restaurant-01" {
		t.Fatal("missing input, server ID or selected note")
	}
}

func TestInvalidModelResultsAreRejected(t *testing.T) {
	for name, content := range map[string]string{
		"foreign note":        strings.ReplaceAll(validResult, "restaurant-01", "restaurant-20"),
		"unknown participant": strings.ReplaceAll(validResult, "user-b", "outsider"),
		"malformed JSON":      "```json\n" + validResult + "\n```",
		"multiple objects":    validResult + validResult,
		"model assigned ID":   strings.Replace(validResult, `"title":`, `"optionId":"forged","title":`, 1),
		"no options":          `{"outcome":"recommended","explanation":"空结果","options":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			provider := testProvider(t, func(w http.ResponseWriter, _ *http.Request) { reply(t, w, content) })
			service, request := testService(t, provider)
			if _, err := service.Generate(context.Background(), request, request.RequesterID); err == nil {
				t.Fatal("invalid model output accepted")
			}
		})
	}
}

func TestTransportFailuresAndCancellation(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			provider := testProvider(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) })
			service, request := testService(t, provider)
			if _, err := service.Generate(context.Background(), request, request.RequesterID); err == nil {
				t.Fatal("HTTP failure ignored")
			}
		})
	}
	provider := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(100 * time.Millisecond):
		}
	})
	service, request := testService(t, provider)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := service.Generate(ctx, request, request.RequesterID); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded, got %v", err)
	}
}

func TestIncompleteResponses(t *testing.T) {
	for _, body := range []string{`not JSON`, `{"done":false}`, `{"done":true,"error":"failed"}`, `{"done":true,"done_reason":"length","message":{"content":"{}"}}`} {
		provider := testProvider(t, func(w http.ResponseWriter, _ *http.Request) {
			if _, err := w.Write([]byte(body)); err != nil {
				t.Error(err)
			}
		})
		service, request := testService(t, provider)
		if _, err := service.Generate(context.Background(), request, request.RequesterID); err == nil {
			t.Fatal("incomplete response accepted")
		}
	}
}
