package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/chat"
	"github.com/LizHu95/Jetaime/services/api/internal/config"
	"github.com/LizHu95/Jetaime/services/api/internal/database"
	"github.com/LizHu95/Jetaime/services/api/internal/llm"
	"github.com/LizHu95/Jetaime/services/api/internal/memory"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Exercise the production module assembly: starting the API starts both memory
// workers even when the main chat Agent is off. All model calls stay local.
func TestPostgresAPIStartsMemoryAutomatically(t *testing.T) {
	connection := os.Getenv("JETAIME_TEST_DATABASE_URL")
	if connection == "" {
		t.Skip("set JETAIME_TEST_DATABASE_URL for PostgreSQL + pgvector startup test")
	}
	t.Setenv("JETAIME_WEB_PUSH_ENABLED", "false")
	t.Setenv("MEMORY_STARTUP_TEST_KEY", "test-only")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	admin, err := database.Open(ctx, connection)
	if err != nil {
		t.Fatal(err)
	}
	schema := "startup_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, e := admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE")
		if e != nil {
			t.Error(e)
		}
		admin.Close()
	})
	u, err := url.Parse(connection)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	pool, err := database.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	alice, bob, group := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if alice > bob {
		alice, bob = bob, alice
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	for _, id := range []string{alice, bob} {
		if _, err = tx.Exec(ctx, `INSERT INTO users(id,nickname) VALUES($1,'Test')`, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO chat_groups(id,user_low_id,user_high_id) VALUES($1,$2,$3)`, group, alice, bob); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO group_members(group_id,user_id) VALUES($1,$2),($1,$3)`, group, alice, bob); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO group_assistants(id,group_id,agent_key) VALUES($1,$2,'main')`, uuid.NewString(), group); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = chat.NewService(chat.NewStore(pool), false).Send(ctx, group, alice, chat.SendInput{Content: "History for automatic memory startup", ClientID: uuid.NewString()}); err != nil {
		t.Fatal(err)
	}
	var extractCalls, embedCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/chat/completions":
			extractCalls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": `{"candidates":[]}`}, "finish_reason": "stop"}}})
		case "/v1/embeddings":
			embedCalls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"index": 0, "embedding": []float32{1, 2, 3}}}})
		default:
			t.Errorf("unexpected model request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	memoryConfig := memory.Config{Embedding: memory.EmbeddingConfig{BaseURL: server.URL + "/v1", Model: "embed-test", Revision: "v1", APIKeyEnv: "MEMORY_STARTUP_TEST_KEY", Dimension: 3, MaxInputBytes: 24576}, Extraction: llm.Config{Provider: "openai-compatible", BaseURL: server.URL + "/v1", Model: "extract-test", APIKeyEnv: "MEMORY_STARTUP_TEST_KEY", OutputMode: "json_object", ContextTokens: 32768}}
	cfg := config.Config{EnableAccountAPI: true, EnableChatAI: false, DatabaseURL: u.String(), Memory: memoryConfig}
	modules, err := buildModules(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); modules.close() }()
	modules.startWorkers(ctx)
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		var ready int
		err = pool.QueryRow(ctx, `SELECT count(*) FROM memory_embeddings WHERE status='ready'`).Scan(&ready)
		if err != nil {
			t.Fatal(err)
		}
		if ready == 1 {
			var cursor int64
			if err = pool.QueryRow(ctx, `SELECT cursor_seq FROM memory_progress WHERE group_id=$1`, group).Scan(&cursor); err != nil {
				t.Fatal(err)
			}
			if cursor != 1 || extractCalls.Load() != 1 || embedCalls.Load() != 1 {
				t.Fatalf("unexpected startup work: cursor=%d extract=%d embed=%d", cursor, extractCalls.Load(), embedCalls.Load())
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("API startup did not process memory automatically")
}
