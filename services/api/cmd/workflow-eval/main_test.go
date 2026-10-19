package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/llm"
)

func TestIsolatedWorkflowUsesRealImplementation(t *testing.T) {
	// This fixture is confined to this protocol test.
	data := []byte(`{"users":[{"id":"lin","nickname":"测试用户"}],"groups":[{"id":"daily","name":"测试群"}],"members":[{"groupId":"daily","userId":"lin"}],"messages":[{"id":"m2","groupId":"daily","senderId":"lin","sentAt":"2026-10-10T10:00:00+08:00","text":"测试消息"}]}`)
	var input request
	if err := json.Unmarshal(data, &input.Fixture); err != nil {
		t.Fatal(err)
	}
	input.GroupID = "daily"
	input.ActorID = "lin"
	input.Kind = "qa"
	input.Question = "测试问题？"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"done": true, "message": map[string]string{"content": `{"status":"answered","answer":"测试消息","sourceMessageIds":["m2"]}`}})
	}))
	defer server.Close()
	for i := 0; i < 2; i++ {
		output, err := execute(context.Background(), input, server.URL, "test", time.Second, 8192)
		if err != nil || output.Error != "" || output.Run.Status != "succeeded" {
			t.Fatalf("%+v %v", output, err)
		}
		for _, step := range output.Run.Steps {
			if step.StartedAt.IsZero() || step.DurationNS < 0 {
				t.Fatal("missing measured timing")
			}
		}
	}
	input.ActorID = "chen"
	output, err := execute(context.Background(), input, server.URL, "test", time.Second, 8192)
	if err != nil || output.Error == "" {
		t.Fatal("unauthorized access not recorded")
	}
}

func TestMainEvaluationUsesRegisteredAgent(t *testing.T) {
	var input request
	if err := json.Unmarshal([]byte(`{"users":[{"id":"u","nickname":"user"}],"groups":[{"id":"g","name":"group"}],"members":[{"groupId":"g","userId":"u"}],"messages":[{"id":"m","groupId":"g","senderId":"u","sentAt":"2026-10-19T10:00:00+08:00","text":"你好"}]}`), &input.Fixture); err != nil {
		t.Fatal(err)
	}
	input.GroupID, input.ActorID, input.Question = "g", "u", "你好"
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(map[string]any{"done": true, "message": map[string]string{"content": `{"action":"answer","basis":"general","answer":{"status":"answered","answer":"你好！","sourceMessageIds":[]}}`}})
	}))
	defer server.Close()
	var previous string
	for _, mode := range []string{"main", "auto"} {
		input.Kind = mode
		out, err := execute(context.Background(), input, server.URL, "test", time.Second, 16384)
		if err != nil || out.Error != "" || out.Run.AgentID != "main" || out.Run.Kind != "main" || out.Run.InvocationID == "" || out.Run.InvocationID == previous {
			t.Fatalf("main evaluation bypassed runtime: %+v %v", out, err)
		}
		previous = out.Run.InvocationID
	}
	if calls != 2 {
		t.Fatalf("ordinary answers made %d model calls", calls)
	}
}

func execute(ctx context.Context, input request, base, model string, timeout time.Duration, tokens int) (result, error) {
	return executeConfigured(ctx, input, llm.Config{Provider: "ollama", BaseURL: base, Model: model, OutputMode: "json_schema", ContextTokens: tokens}, timeout)
}

func TestCloudWorkflowSharesStartupConfiguration(t *testing.T) {
	t.Setenv("TEST_EVAL_KEY", "test-key")
	var input request
	if err := json.Unmarshal([]byte(`{"users":[{"id":"u","nickname":"测试用户"}],"groups":[{"id":"g","name":"测试群"}],"members":[{"groupId":"g","userId":"u"}],"messages":[{"id":"m","groupId":"g","senderId":"u","sentAt":"2026-10-14T10:00:00+08:00","text":"测试消息"}]}`), &input.Fixture); err != nil {
		t.Fatal(err)
	}
	input.GroupID, input.ActorID, input.Kind, input.Question = "g", "u", "qa", "测试问题"
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Error("wrong cloud route")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"role": "assistant", "content": `{"status":"answered","answer":"测试消息","sourceMessageIds":["m"]}`}, "finish_reason": "stop"}}})
	}))
	defer s.Close()
	c := llm.Config{Provider: "openai-compatible", BaseURL: s.URL + "/v1", Model: "cloud-test", APIKeyEnv: "TEST_EVAL_KEY", OutputMode: "json_object", ContextTokens: 8192, Profile: "cloud"}
	out, err := executeConfigured(context.Background(), input, c, time.Second)
	if err != nil || out.Error != "" || out.Run.Status != "succeeded" {
		t.Fatalf("cloud workflow failed: %+v %v", out, err)
	}
	if out.Run.Model != "cloud-test" || out.Run.ModelIdentity == nil || out.Run.ModelIdentity.Provider != "openai-compatible" || out.Run.ModelIdentity.Profile != "cloud" {
		t.Fatal("selected model identity lost")
	}
}

func TestCloudExtractionAndReviewUseSameModel(t *testing.T) {
	t.Setenv("TEST_EVAL_KEY", "test-key")
	var input request
	if err := json.Unmarshal([]byte(`{"users":[{"id":"u","nickname":"测试用户"}],"groups":[{"id":"g","name":"测试群"}],"members":[{"groupId":"g","userId":"u"}],"messages":[{"id":"m","groupId":"g","senderId":"u","sentAt":"2026-10-14T10:00:00+08:00","text":"我明天整理资料"}]}`), &input.Fixture); err != nil {
		t.Fatal(err)
	}
	input.GroupID, input.ActorID, input.Kind = "g", "u", "extract"
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body struct {
			Model          string `json:"model"`
			ResponseFormat struct {
				JSONSchema struct {
					Schema struct {
						Properties map[string]any `json:"properties"`
					} `json:"schema"`
				} `json:"json_schema"`
			} `json:"response_format"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Model != "cloud-test" {
			t.Error("model changed between workflow stages")
		}
		content := `{"suggestions":[{"title":"整理资料","assigneeId":"u","timeExpression":"明天","sourceMessageIds":["m"]}]}`
		if body.ResponseFormat.JSONSchema.Schema.Properties["reviews"] != nil {
			content = `{"reviews":[{"candidateIndex":0,"evidenceMessageIds":["m"],"reason":"本人明确安排，没有后续完成证据","status":"active"}]}`
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"role": "assistant", "content": content}, "finish_reason": "stop"}}})
	}))
	defer s.Close()
	c := llm.Config{Provider: "openai-compatible", BaseURL: s.URL + "/v1", Model: "cloud-test", APIKeyEnv: "TEST_EVAL_KEY", OutputMode: "json_schema", ContextTokens: 32768}
	out, err := executeConfigured(context.Background(), input, c, time.Second)
	if err != nil || out.Error != "" || out.Run.Status != "succeeded" || len(out.Run.Suggestions) != 1 || calls != 2 {
		t.Fatalf("extraction/review failed: %+v %v, calls=%d", out, err, calls)
	}
}
