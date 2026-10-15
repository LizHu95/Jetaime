package webpush

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/auth"
	"github.com/LizHu95/Jetaime/services/api/internal/chat"
	"github.com/LizHu95/Jetaime/services/api/internal/contacts"
	"github.com/LizHu95/Jetaime/services/api/internal/database"
	push "github.com/SherClockHolmes/webpush-go"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func subscription(t *testing.T, suffix string) push.Subscription {
	t.Helper()
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return push.Subscription{Endpoint: "https://web.push.apple.com/" + suffix, Keys: push.Keys{P256dh: base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), Auth: base64.RawURLEncoding.EncodeToString(make([]byte, 16))}}
}
func TestSubscriptionValidation(t *testing.T) {
	s := subscription(t, "valid")
	if !validSubscription(s) {
		t.Fatal("valid Apple subscription rejected")
	}
	for _, endpoint := range []string{"http://web.push.apple.com/x", "https://127.0.0.1/x", "https://web.push.apple.com.attacker.test/x", "https://web.push.apple.com:8080/x", "https://u:p@web.push.apple.com/x"} {
		s.Endpoint = endpoint
		if validSubscription(s) {
			t.Fatalf("unsafe endpoint accepted: %s", endpoint)
		}
	}
	s = subscription(t, "bad-key")
	s.Keys.P256dh = base64.RawURLEncoding.EncodeToString(make([]byte, 65))
	if validSubscription(s) {
		t.Fatal("invalid curve point accepted")
	}
}

type fixture struct {
	pool     *pgxpool.Pool
	accounts *auth.Service
	users    []auth.User
	tokens   []string
	group    string
	mux      *http.ServeMux
	config   Config
}

func setup(t *testing.T) fixture {
	t.Helper()
	uri := os.Getenv("JETAIME_TEST_DATABASE_URL")
	if uri == "" {
		t.Skip("set JETAIME_TEST_DATABASE_URL for isolated PostgreSQL tests")
	}
	ctx := context.Background()
	admin, err := database.Open(ctx, uri)
	if err != nil {
		t.Fatal(err)
	}
	schema := pgx.Identifier{"push_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); admin.Close() })
	cfg, err := pgxpool.ParseConfig(uri)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = strings.Trim(schema, "\"")
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err = database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	accounts, err := auth.NewService(auth.NewPostgresStore(pool))
	if err != nil {
		t.Fatal(err)
	}
	private, public, err := push.GenerateVAPIDKeys()
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{pool: pool, accounts: accounts, config: Config{Enabled: true, PrivateKey: private, PublicKey: public, Subject: "https://example.com/"}, mux: http.NewServeMux()}
	for _, name := range []string{"Alice", "Bob"} {
		u, e := accounts.Register(ctx, name, "test-password", name)
		if e != nil {
			t.Fatal(e)
		}
		l, e := accounts.Login(ctx, name, "test-password")
		if e != nil {
			t.Fatal(e)
		}
		f.users = append(f.users, u)
		f.tokens = append(f.tokens, l.Token)
	}
	friends := contacts.NewService(contacts.NewPostgresStore(pool))
	request, err := friends.Send(ctx, f.users[0].ID, "Bob")
	if err != nil {
		t.Fatal(err)
	}
	request, err = friends.Resolve(ctx, f.users[1].ID, request.ID, "accept")
	if err != nil {
		t.Fatal(err)
	}
	f.group = *request.GroupID
	NewHandler(pool, auth.NewHandler(accounts), f.config).RegisterRoutes(f.mux)
	return f
}
func (f fixture) request(t *testing.T, method, path, token string, body any) int {
	t.Helper()
	data, _ := json.Marshal(body)
	r := httptest.NewRequest(method, path, strings.NewReader(string(data)))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	return w.Code
}
func (f fixture) subscribe(t *testing.T, user int, s push.Subscription) {
	t.Helper()
	if code := f.request(t, "POST", "/api/push/subscriptions", f.tokens[user], map[string]any{"endpoint": s.Endpoint, "keys": s.Keys, "expirationTime": nil}); code != 204 {
		t.Fatalf("subscribe: %d", code)
	}
}
func (f fixture) count(t *testing.T, sql string) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), sql).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

type fakeClient struct {
	status int
	calls  int
}

func (c *fakeClient) Do(r *http.Request) (*http.Response, error) {
	c.calls++
	if !strings.HasPrefix(r.Header.Get("Authorization"), "vapid ") {
		return nil, fmt.Errorf("missing VAPID")
	}
	return &http.Response{StatusCode: c.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
}

func TestPostgresOutboxAndDelivery(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	a, b := subscription(t, "alice"), subscription(t, "bob")
	f.subscribe(t, 0, a)
	f.subscribe(t, 1, b)
	if code := f.request(t, "POST", "/api/push/subscriptions", "", a); code != 401 {
		t.Fatal("anonymous subscription accepted")
	}
	if code := f.request(t, "POST", "/api/push/test", f.tokens[0], map[string]string{"endpoint": b.Endpoint}); code != 429 {
		t.Fatal("cross-account test accepted")
	}
	input := chat.SendInput{Content: "private message", ClientID: uuid.NewString(), AgentKind: "qa"}
	store := chat.NewStore(f.pool)
	m, err := store.Send(ctx, f.group, f.users[0].ID, input, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Send(ctx, f.group, f.users[0].ID, input, false); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT count(*) FROM web_push_jobs`); n != 1 {
		t.Fatalf("ordinary message must enqueue only receiver once: %d", n)
	}
	client := &fakeClient{status: 503}
	worker := NewWorker(f.pool, f.config)
	worker.client = client
	if !worker.deliver(ctx) {
		t.Fatal("job not claimed")
	}
	if n := f.count(t, `SELECT count(*) FROM web_push_jobs WHERE attempts=1 AND completed_at IS NULL AND available_at>now()`); n != 1 {
		t.Fatal("transient failure not retried")
	}
	_, _ = f.pool.Exec(ctx, `UPDATE web_push_jobs SET available_at=now()`)
	client.status = 201
	if !worker.deliver(ctx) {
		t.Fatal("retry not claimed")
	}
	if n := f.count(t, `SELECT count(*) FROM web_push_jobs WHERE completed_at IS NOT NULL`); n != 1 {
		t.Fatal("success not persisted")
	}
	// AI replies enqueue for both participants, in the same database statement.
	_, err = f.pool.Exec(ctx, `WITH seq AS (UPDATE chat_groups SET last_message_seq=last_message_seq+1 WHERE id=$1 RETURNING last_message_seq)
 INSERT INTO messages(id,group_id,seq,sender_type,sender_assistant_id,content) SELECT $2,$1,seq.last_message_seq,'assistant',a.id,'AI answer' FROM seq,group_assistants a WHERE a.group_id=$1`, f.group, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT count(*) FROM web_push_jobs WHERE completed_at IS NULL`); n != 2 {
		t.Fatalf("AI recipient count: %d", n)
	}
	client.status = 410
	if !worker.deliver(ctx) {
		t.Fatal("expired endpoint not claimed")
	}
	if n := f.count(t, `SELECT count(*) FROM web_push_subscriptions`); n != 1 {
		t.Fatal("410 did not remove expired subscription")
	}
	if m.ID == "" {
		t.Fatal("missing message")
	}
}

func TestPostgresRebindLogoutAndRollback(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	s := subscription(t, "shared-device")
	f.subscribe(t, 1, s)
	// Queue a test, rate-limit repeats, then transfer the device to another account.
	if code := f.request(t, "POST", "/api/push/test", f.tokens[1], map[string]string{"endpoint": s.Endpoint}); code != 202 {
		t.Fatalf("test: %d", code)
	}
	if code := f.request(t, "POST", "/api/push/test", f.tokens[1], map[string]string{"endpoint": s.Endpoint}); code != 429 {
		t.Fatal("test not rate limited")
	}
	f.subscribe(t, 0, s)
	wrongKeys := subscription(t, "shared-device")
	if code := f.request(t, "POST", "/api/push/subscriptions", f.tokens[1], wrongKeys); code != 409 {
		t.Fatal("endpoint takeover with different keys accepted")
	}
	if n := f.count(t, `SELECT count(*) FROM web_push_jobs`); n != 0 {
		t.Fatal("account switch kept old queued notifications")
	}
	if code := f.request(t, "DELETE", "/api/push/subscriptions", f.tokens[1], map[string]string{"endpoint": s.Endpoint}); code != 204 {
		t.Fatal(code)
	}
	if n := f.count(t, `SELECT count(*) FROM web_push_subscriptions`); n != 1 {
		t.Fatal("other account deleted subscription")
	}
	if code := f.request(t, "POST", "/api/push/test", f.tokens[0], map[string]string{"endpoint": s.Endpoint}); code != 202 {
		t.Fatal(code)
	}
	if err := f.accounts.Logout(ctx, f.tokens[0]); err != nil {
		t.Fatal(err)
	}
	worker := NewWorker(f.pool, f.config)
	client := &fakeClient{status: 201}
	worker.client = client
	if worker.deliver(ctx) || client.calls != 0 {
		t.Fatal("revoked session received notification")
	}
	// New device for Bob: a rolled-back message must not leave an outbox row.
	f.subscribe(t, 1, subscription(t, "new-bob"))
	before := f.count(t, `SELECT count(*) FROM web_push_jobs`)
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(ctx, `WITH seq AS (UPDATE chat_groups SET last_message_seq=last_message_seq+1 WHERE id=$1 RETURNING last_message_seq)
 INSERT INTO messages(id,group_id,seq,sender_type,sender_user_id,content,client_message_id) SELECT $2,$1,seq.last_message_seq,'user',$3,'rollback',$4 FROM seq`, f.group, uuid.NewString(), f.users[0].ID, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	_ = tx.Rollback(ctx)
	if n := f.count(t, `SELECT count(*) FROM web_push_jobs`); n != before {
		t.Fatal("rollback leaked notification")
	}
	// Expired sessions are also filtered at delivery, even before periodic cleanup.
	if code := f.request(t, "POST", "/api/push/test", f.tokens[1], map[string]string{"endpoint": "https://web.push.apple.com/new-bob"}); code != 202 {
		t.Fatal(code)
	}
	_, _ = f.pool.Exec(ctx, `UPDATE auth_sessions SET expires_at=now()+interval '1 second' WHERE user_id=$1`, f.users[1].ID)
	time.Sleep(1100 * time.Millisecond)
	if worker.deliver(ctx) {
		t.Fatal("expired session delivery")
	}
}
