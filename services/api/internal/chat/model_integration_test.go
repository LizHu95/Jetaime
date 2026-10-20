package chat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
	"github.com/LizHu95/Jetaime/services/api/internal/auth"
	"github.com/LizHu95/Jetaime/services/api/internal/httpapi"
	"github.com/LizHu95/Jetaime/services/api/internal/notification/realtime"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// Opt-in real local model acceptance. setup creates and drops its own schema;
// no fixture history or test accounts are written into the application schema.
func TestPostgresOllamaAgentPush(t *testing.T) {
	if os.Getenv("JETAIME_TEST_OLLAMA") != "1" {
		t.Skip("set JETAIME_TEST_OLLAMA=1 for real Ollama acceptance")
	}
	testModelAgentPush(t, llm.Config{Provider: "ollama", BaseURL: "http://127.0.0.1:11434", Model: "qwen3.5:9b", ContextTokens: 16384}, "JETAIME_OLLAMA_CHECK_OUTPUT")
}

func TestPostgresCloudAgentPush(t *testing.T) {
	if os.Getenv("JETAIME_TEST_CLOUD") != "1" {
		t.Skip("set JETAIME_TEST_CLOUD=1 for real cloud acceptance")
	}
	path := os.Getenv("JETAIME_CLOUD_MODEL_CONFIG")
	if path == "" {
		t.Fatal("set JETAIME_CLOUD_MODEL_CONFIG to a model profile file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("could not read model configuration")
	}
	var profiles struct {
		Models map[string]llm.Config `json:"models"`
	}
	if err = json.Unmarshal(data, &profiles); err != nil {
		t.Fatal("invalid model configuration")
	}
	config, ok := profiles.Models["cloud-qwen"]
	if !ok || config.Provider != "openai-compatible" {
		t.Fatal("cloud-qwen compatible profile required")
	}
	if model := os.Getenv("JETAIME_TEST_CLOUD_MODEL"); model != "" {
		config.Model = model
	}
	if config.Model == "YOUR_MODEL_OR_DEPLOYMENT_ID" {
		t.Fatal("real cloud model ID required")
	}
	config.Profile = "cloud-qwen"
	testModelAgentPush(t, config, "JETAIME_CLOUD_CHECK_OUTPUT")
}

func testModelAgentPush(t *testing.T, config llm.Config, outputEnv string) {
	t.Helper()
	started := time.Now()
	f := setup(t)
	provider, err := config.New(2 * time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var callsMu sync.Mutex
	var calls []map[string]any
	traced := testModel{func(ctx context.Context, request llm.Request) (llm.Result, error) {
		callStarted := time.Now()
		output, err := provider.Call(ctx, request)
		callsMu.Lock()
		calls = append(calls, map[string]any{"durationMs": time.Since(callStarted).Milliseconds(), "promptTokens": output.PromptTokens, "completionTokens": output.CompletionTokens, "succeeded": err == nil})
		callsMu.Unlock()
		t.Logf("%s model output (test records): %.2000s error=%v", config.Model, output.Content, err)
		return output, err
	}}
	f.configureAI(traced, config.Model, config.ContextTokens, 2*time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rt := realtime.New(ctx, f.store, f.auth)
	go rt.Listen(ctx)
	go f.worker(t).Work(ctx)
	router := httpapi.NewRouter(httpapi.Handlers{Chat: NewHandler(f.service, auth.NewHandler(f.auth), rt)})
	server := httptest.NewServer(router)
	defer server.Close()
	connect := func(token string) *websocket.Conn {
		t.Helper()
		conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/api/chat/events", http.Header{"Origin": []string{server.URL}})
		if err != nil {
			t.Fatal(err)
		}
		if err = conn.WriteJSON(map[string]string{"type": "authenticate", "token": token}); err != nil {
			t.Fatal(err)
		}
		var event Event
		if err = conn.ReadJSON(&event); err != nil || event.Type != "ready" {
			t.Fatal("socket authentication", event, err)
		}
		return conn
	}
	a, b := connect(f.tokens[0]), connect(f.tokens[1])
	defer func() { _ = a.Close(); _ = b.Close() }()
	send := func(actor int, text string) Message {
		t.Helper()
		body, _ := json.Marshal(SendInput{Content: text, ClientID: uuid.NewString()})
		request := httptest.NewRequest("POST", "/api/chat/groups/"+f.group+"/messages", strings.NewReader(string(body)))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+f.tokens[actor])
		out := httptest.NewRecorder()
		router.ServeHTTP(out, request)
		if out.Code != 200 {
			t.Fatalf("send %d %s", out.Code, out.Body.String())
		}
		var message Message
		if err := json.Unmarshal(out.Body.Bytes(), &message); err != nil {
			t.Fatal(err)
		}
		return message
	}
	awaitMessage := func(conn *websocket.Conn) {
		t.Helper()
		_ = conn.SetReadDeadline(time.Now().Add(150 * time.Second))
		for {
			var event Event
			if err := conn.ReadJSON(&event); err != nil {
				t.Fatal("missing real message push", err)
			}
			if event.Type == "ai" {
				runs, err := f.store.Runs(ctx, f.group, f.users[0].ID)
				if err == nil && len(runs) > 0 && runs[0].Status == "failed" {
					t.Fatalf("Agent rejected response: %+v", runs[0])
				}
			}
			if event.Type == "message" {
				if event.GroupID != f.group {
					t.Fatal("wrong push group")
				}
				return
			}
		}
	}
	// The listener reconnect hint precedes these commits; give it a bounded
	// moment to finish its dedicated LISTEN connection before testing delivery.
	time.Sleep(200 * time.Millisecond)
	fact := send(0, "我们确定周末预算是200元，周日下午三点出发。")
	for _, conn := range []*websocket.Conn{a, b} {
		awaitMessage(conn)
	}
	send(1, "好的，预算200元，周日下午三点出发。")
	for _, conn := range []*websocket.Conn{a, b} {
		awaitMessage(conn)
	}
	runs, _ := f.store.Runs(ctx, f.group, f.users[0].ID)
	if len(runs) != 0 {
		t.Fatal("ordinary message invoked model")
	}
	trigger := send(0, "@AI 我们确定的周末预算是多少？")
	for _, conn := range []*websocket.Conn{a, b} {
		awaitMessage(conn)
	}
	t.Log("@AI request saved; waiting for original Agent and real model reply")
	for _, conn := range []*websocket.Conn{a, b} {
		awaitMessage(conn)
	}
	page, err := f.store.Messages(ctx, f.group, f.users[1].ID, trigger.Seq, 0, 100)
	if err != nil || len(page.Messages) != 1 {
		t.Fatal("reply not committed before push", page, err)
	}
	reply := page.Messages[0]
	if reply.SenderType != "assistant" || reply.AgentResult == nil || reply.AgentResult.Answer == nil || reply.AgentResult.Answer.Status != "answered" || !strings.Contains(reply.Content, "200") {
		t.Fatal("real Agent answer invalid", reply)
	}
	if len(reply.AgentResult.Answer.SourceMessageIDs) == 0 {
		t.Fatal("answer omitted verified sources")
	}
	sources := map[string]bool{fact.ID: true}
	all, _ := f.store.Messages(ctx, f.group, f.users[0].ID, 0, 0, 100)
	for _, message := range all.Messages {
		if message.Seq < trigger.Seq {
			sources[message.ID] = true
		}
	}
	for _, id := range reply.AgentResult.Answer.SourceMessageIDs {
		if !sources[id] {
			t.Fatal("answer cited something other than historical evidence", id)
		}
	}
	t.Logf("Real model reply: %s; verified source IDs: %v", reply.Content, reply.AgentResult.Answer.SourceMessageIDs)

	// Exercise the same original extraction + independent evidence review from
	// a message trigger. Suggestions are replies, never auto-created tasks.
	task := send(0, "我明天负责准备周末聚餐的食材。")
	for _, conn := range []*websocket.Conn{a, b} {
		awaitMessage(conn)
	}
	extraction := send(0, "@AI 请提取待办")
	for _, conn := range []*websocket.Conn{a, b} {
		awaitMessage(conn)
	}
	t.Log("waiting for original extraction and evidence review on real model")
	for _, conn := range []*websocket.Conn{a, b} {
		awaitMessage(conn)
	}
	extractPage, err := f.store.Messages(ctx, f.group, f.users[1].ID, extraction.Seq, 0, 100)
	if err != nil || len(extractPage.Messages) != 1 || extractPage.Messages[0].AgentResult == nil {
		t.Fatal("real extraction not pushed", extractPage, err)
	}
	extractReply := extractPage.Messages[0]
	found := false
	for _, suggestion := range extractReply.AgentResult.Suggestions {
		if suggestion.AssigneeID != nil && *suggestion.AssigneeID == f.users[0].ID && strings.Contains(suggestion.Title, "食材") {
			for _, source := range suggestion.SourceMessageIDs {
				if source == task.ID {
					found = true
				}
			}
		}
	}
	if !found || !strings.Contains(extractReply.Content, "尚未创建") {
		t.Fatal("real extraction lost evidence/owner", extractReply)
	}
	t.Logf("Real model extraction: %s", extractReply.Content)
	if output := os.Getenv(outputEnv); output != "" {
		callsMu.Lock()
		result := map[string]any{"provider": config.Provider, "model": config.Model, "contextTokens": config.ContextTokens, "durationMs": time.Since(started).Milliseconds(), "calls": calls, "triggerMessageId": trigger.ID, "response": reply, "pushedToBoth": true, "ordinaryMessagesDidNotInvokeAI": true, "extractionResponse": extractReply}
		data, _ := json.MarshalIndent(result, "", "  ")
		callsMu.Unlock()
		if err := os.MkdirAll(filepath.Dir(output), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(output, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
