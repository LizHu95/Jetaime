package chat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/bootstrap"
	aicontext "github.com/LizHu95/Jetaime/services/api/internal/ai/context"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/jobs"
	"github.com/LizHu95/Jetaime/services/api/internal/auth"
	"github.com/LizHu95/Jetaime/services/api/internal/collaboration"
	"github.com/LizHu95/Jetaime/services/api/internal/database"
	"github.com/LizHu95/Jetaime/services/api/internal/httpapi"
	"github.com/LizHu95/Jetaime/services/api/internal/llm"
	"github.com/LizHu95/Jetaime/services/api/internal/notification/realtime"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func chatTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("JETAIME_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set JETAIME_TEST_DATABASE_URL for PostgreSQL auth integration tests")
	}
	ctx := context.Background()
	admin, err := database.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	schema := "chat_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
		admin.Close()
	})
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return pool
}

type testModel struct {
	call func(context.Context, llm.Request) (llm.Result, error)
}

func (m testModel) Call(ctx context.Context, r llm.Request) (llm.Result, error) {
	return m.call(ctx, r)
}

type fixture struct {
	model     llm.ChatModel
	capacity  int
	modelName string
	timeout   time.Duration
	injector  aicontext.ContextInjector

	store   *Store
	service *Service
	auth    *auth.Service
	users   []auth.User
	tokens  []string
	group   string
}

func setup(t *testing.T) fixture {
	t.Helper()
	ctx := context.Background()
	pool := chatTestPool(t)
	accounts, err := auth.NewService(auth.NewPostgresStore(pool))
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{store: NewStore(pool), auth: accounts}
	for _, name := range []string{"Alice", "Bob", "Carol"} {
		u, e := accounts.Register(ctx, name, "pass123", name)
		if e != nil {
			t.Fatal(e)
		}
		login, e := accounts.Login(ctx, name, "pass123")
		if e != nil {
			t.Fatal(e)
		}
		f.users = append(f.users, u)
		f.tokens = append(f.tokens, login.Token)
	}
	friends := collaboration.NewService(collaboration.NewPostgresStore(pool))
	r, err := friends.Send(ctx, f.users[0].ID, "Bob")
	if err != nil {
		t.Fatal(err)
	}
	r, err = friends.Resolve(ctx, f.users[1].ID, r.ID, "accept")
	if err != nil {
		t.Fatal(err)
	}
	f.group = *r.GroupID
	model := testModel{func(_ context.Context, r llm.Request) (llm.Result, error) {
		var payload struct {
			Context struct {
				Messages []struct {
					ID string `json:"id"`
				} `json:"messages"`
			} `json:"context"`
		}
		if err := json.Unmarshal([]byte(r.Messages[1].Content), &payload); err != nil {
			return llm.Result{}, err
		}
		data, err := json.Marshal(map[string]any{"status": "answered", "answer": "共享 AI 回答", "sourceMessageIds": []string{payload.Context.Messages[0].ID}})
		if strings.Contains(r.Messages[0].Content, "主Agent") {
			data, err = json.Marshal(map[string]any{"action": "answer", "basis": "history", "answer": json.RawMessage(data)})
		}
		return llm.Result{Content: string(data)}, err
	}}
	f.configureAI(model, "test", 8192, time.Second)
	return f
}
func TestPostgresMessagesAndRead(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	input := SendInput{Content: "hello", ClientID: uuid.NewString()}
	m, err := f.service.Send(ctx, f.group, f.users[0].ID, input)
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.service.Send(ctx, f.group, f.users[0].ID, input)
	if err != nil || m.ID != again.ID {
		t.Fatal("retry duplicated message", err)
	}
	input.Content = "changed"
	if _, err = f.service.Send(ctx, f.group, f.users[0].ID, input); !errors.Is(err, ErrConflict) {
		t.Fatal("changed retry accepted")
	}
	if _, err = f.store.Messages(ctx, f.group, f.users[2].ID, 0, 0, 100); !errors.Is(err, ErrNotFound) {
		t.Fatal("messages leaked")
	}
	if _, err = f.service.Send(ctx, f.group, f.users[2].ID, SendInput{Content: "spoof", ClientID: uuid.NewString()}); !errors.Is(err, ErrNotFound) {
		t.Fatal("third-party sent")
	}
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := range 12 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, e := f.service.Send(ctx, f.group, f.users[i%2].ID, SendInput{Content: "并发消息", ClientID: uuid.NewString()})
			errs <- e
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	page, err := f.store.Messages(ctx, f.group, f.users[0].ID, 0, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 13 {
		t.Fatal("missing messages")
	}
	for i, m := range page.Messages {
		if m.Seq != int64(i+1) {
			t.Fatal("sequence gap")
		}
	}
	state, err := f.store.State(ctx, f.group, f.users[0].ID)
	if err != nil || state.Unread != 6 {
		t.Fatalf("own messages counted: %+v %v", state, err)
	}
	if read, err := f.store.Read(ctx, f.group, f.users[0].ID, 999); err != nil || read != 13 {
		t.Fatal("read cursor not capped", read, err)
	}
	if read, err := f.store.Read(ctx, f.group, f.users[0].ID, 2); err != nil || read != 13 {
		t.Fatal("read cursor moved backwards", read, err)
	}
	state, _ = f.store.State(ctx, f.group, f.users[0].ID)
	if state.Unread != 0 {
		t.Fatal("unread not cleared")
	}
	page, err = f.store.Messages(ctx, f.group, f.users[0].ID, 0, 8, 3)
	if err != nil || !page.HasMore || len(page.Messages) != 3 || page.Messages[0].Seq != 5 || page.Messages[2].Seq != 7 {
		t.Fatal("history page wrong", page, err)
	}
	page, err = f.store.Messages(ctx, f.group, f.users[0].ID, 10, 0, 100)
	if err != nil || len(page.Messages) != 3 {
		t.Fatal("catchup page wrong")
	}
	if err = f.store.Close(ctx, f.group, f.users[1].ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.Messages(ctx, f.group, f.users[0].ID, 0, 0, 100); !errors.Is(err, ErrNotFound) {
		t.Fatal("closed messages accessible")
	}
	if _, err = f.service.Send(ctx, f.group, f.users[0].ID, SendInput{Content: "late", ClientID: uuid.NewString()}); !errors.Is(err, ErrNotFound) {
		t.Fatal("closed group accepted message")
	}
}
func TestPostgresAIQueueFencingAndContext(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	for _, text := range []string{"普通消息", "@AI 第一问", "@AI 第二问", "future secret"} {
		if _, err := f.service.Send(ctx, f.group, f.users[0].ID, SendInput{Content: text, ClientID: uuid.NewString()}); err != nil {
			t.Fatal(err)
		}
	}
	runs, err := f.store.Runs(ctx, f.group, f.users[1].ID)
	if err != nil || len(runs) != 2 {
		t.Fatal("ordinary message invoked AI", runs, err)
	}
	first, err := f.store.Claim(ctx, time.Minute)
	if err != nil || first.ContextEnd != 2 {
		t.Fatal("queue order", first, err)
	}
	if _, err := f.store.Claim(ctx, time.Minute); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("group ran concurrently")
	}
	snapshot, _, err := f.store.AgentContext(ctx, first, 8192)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range snapshot.Messages {
		if strings.Contains(m.Text, "第二问") || strings.Contains(m.Text, "future secret") {
			t.Fatal("future context leaked")
		}
	}
	if err = f.store.Complete(ctx, first, "AI 第一条回答", ""); err != nil {
		t.Fatal(err)
	}
	if err = f.store.Complete(ctx, first, "duplicate", ""); !errors.Is(err, ErrConflict) {
		t.Fatal("answer duplicated")
	}
	state, _ := f.store.State(ctx, f.group, f.users[0].ID)
	if state.Unread != 1 {
		t.Fatal("AI response did not count as incoming")
	}
	second, err := f.store.Claim(ctx, time.Minute)
	if err != nil || second.ContextEnd != 3 {
		t.Fatal("second queued question missing")
	}
	if err = f.store.Complete(ctx, second, "", "model_failed"); err != nil {
		t.Fatal(err)
	}
	if err = f.store.Retry(ctx, f.group, f.users[1].ID, second.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("peer retried other user's request")
	}
	if err = f.store.Retry(ctx, f.group, f.users[0].ID, second.ID); err != nil {
		t.Fatal(err)
	}
	retry, err := f.store.Claim(ctx, time.Minute)
	if err != nil || retry.ID != second.ID || retry.Attempt != 2 {
		t.Fatal("retry created a different task")
	}
	if err = f.store.Complete(ctx, second, "stale worker", ""); !errors.Is(err, ErrConflict) {
		t.Fatal("stale attempt wrote")
	}
	if err = f.store.Close(ctx, f.group, f.users[1].ID); err != nil {
		t.Fatal(err)
	}
	if err = f.store.Complete(ctx, retry, "late reply", ""); !errors.Is(err, ErrNotFound) {
		t.Fatal("closed group accepted AI reply")
	}
	var count int
	if err = f.store.pool.QueryRow(ctx, `SELECT count(*) FROM messages WHERE sender_type='assistant'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("wrong AI answer count", count, err)
	}
}
func TestPostgresAIExecutionAndLeaseRecovery(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	_, err := f.service.Send(ctx, f.group, f.users[0].ID, SendInput{Content: "@AI 回复", ClientID: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	expired, err := f.store.Claim(ctx, -time.Second)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := f.store.Claim(ctx, time.Minute)
	if err != nil || recovered.ID != expired.ID || recovered.Attempt != 2 {
		t.Fatal("lease not recovered")
	}
	f.worker(t).Execute(ctx, recovered)
	runs, _ := f.store.Runs(ctx, f.group, f.users[0].ID)
	if runs[0].Status != "succeeded" {
		t.Fatal("model response not saved", runs)
	}
	page, _ := f.store.Messages(ctx, f.group, f.users[1].ID, 0, 0, 100)
	if len(page.Messages) != 2 || page.Messages[1].Content != "共享 AI 回答" || page.Messages[1].ReplyToID == nil {
		t.Fatal("AI reply mapping wrong")
	}
}
func TestPostgresRealtimeIsolationAndRevocation(t *testing.T) {
	f := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rt := realtime.New(ctx, f.store, f.auth)
	mux := httpapi.NewRouter(NewHandler(f.service, auth.NewHandler(f.auth), rt))
	server := httptest.NewServer(mux)
	defer server.Close()
	dial := func(token string) *websocket.Conn {
		t.Helper()
		url := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/chat/events"
		conn, _, err := websocket.DefaultDialer.Dial(url, http.Header{"Origin": []string{server.URL}})
		if err != nil {
			t.Fatal(err)
		}
		if err = conn.WriteJSON(map[string]string{"type": "authenticate", "token": token}); err != nil {
			t.Fatal(err)
		}
		var event Event
		if err = conn.ReadJSON(&event); err != nil || event.Type != "ready" {
			t.Fatal("not authenticated", event, err)
		}
		return conn
	}
	a := dial(f.tokens[0])
	defer func() { _ = a.Close() }()
	b := dial(f.tokens[1])
	defer func() { _ = b.Close() }()
	c := dial(f.tokens[2])
	defer func() { _ = c.Close() }()
	// Start listening after authentication so every client sees the initial resync.
	go rt.Listen(ctx)
	for _, conn := range []*websocket.Conn{a, b, c} {
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		var event Event
		if err := conn.ReadJSON(&event); err != nil || event.Type != "resync" {
			t.Fatal("listener did not become ready", event, err)
		}
	}

	_, err := f.service.Send(ctx, f.group, f.users[0].ID, SendInput{Content: "realtime", ClientID: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	_ = b.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		var event Event
		if err = b.ReadJSON(&event); err != nil {
			t.Fatal(err)
		}
		if event.Type == "message" {
			break
		}
	}
	if err = f.auth.Logout(ctx, f.tokens[1]); err != nil {
		t.Fatal(err)
	}
	_ = b.SetReadDeadline(time.Now().Add(4 * time.Second))
	_, _, err = b.ReadMessage()
	var closeErr *websocket.CloseError
	if !errors.As(err, &closeErr) || closeErr.Code != 1008 {
		t.Fatal("revoked socket still active", err)
	}
	_ = c.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	for {
		var event Event
		err = c.ReadJSON(&event)
		if err != nil {
			break
		}
		if event.GroupID == f.group {
			t.Fatal("third-party received group event")
		}
	}
	r := httptest.NewRequest("POST", "/api/chat/groups/"+f.group+"/read", strings.NewReader(`{"seq":1}`))
	out := httptest.NewRecorder()
	mux.ServeHTTP(out, r)
	if out.Code != 401 {
		t.Fatal("HTTP business auth bypass")
	}
	invalid := httptest.NewRequest("GET", "/api/chat/groups/not-a-uuid", nil)
	invalid.Header.Set("Authorization", "Bearer "+f.tokens[0])
	out = httptest.NewRecorder()
	mux.ServeHTTP(out, invalid)
	if out.Code != 400 {
		t.Fatal("invalid group leaked DB error")
	}
	encoded, _ := json.Marshal(Event{Type: "read", GroupID: f.group})
	if strings.Contains(string(encoded), "token") {
		t.Fatal("notification leaked credentials")
	}
}

func TestPostgresAIInvalidOutputAndBoundaries(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	for _, answer := range []string{`{"answer":"\u0000"}`, `{"answer":""}`, `{"answer":"ok","other":1}`, `not json`} {
		f.model = testModel{func(context.Context, llm.Request) (llm.Result, error) { return llm.Result{Content: answer}, nil }}
		_, err := f.service.Send(ctx, f.group, f.users[0].ID, SendInput{Content: "@AI test", ClientID: uuid.NewString(), AgentKind: "qa"})
		if err != nil {
			t.Fatal(err)
		}
		run, err := f.store.Claim(ctx, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		f.worker(t).Execute(ctx, run)
		runs, err := f.store.Runs(ctx, f.group, f.users[0].ID)
		if err != nil || runs[0].Status != "failed" || runs[0].ErrorCode == nil || *runs[0].ErrorCode != "invalid_model_response" {
			t.Fatal("invalid model output persisted", runs, err)
		}
	}
	if _, err := f.service.Send(ctx, f.group, f.users[0].ID, SendInput{Content: "@AI large", ClientID: uuid.NewString()}); err != nil {
		t.Fatal(err)
	}
	run, err := f.store.Claim(ctx, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.store.AgentContext(ctx, run, 100); !errors.Is(err, ErrContext) {
		t.Fatal("large context silently truncated")
	}
	// A second group's context contains none of the first group's conversation.
	friends := collaboration.NewService(collaboration.NewPostgresStore(f.store.pool))
	request, err := friends.Send(ctx, f.users[0].ID, "Carol")
	if err != nil {
		t.Fatal(err)
	}
	request, err = friends.Resolve(ctx, f.users[2].ID, request.ID, "accept")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.Send(ctx, *request.GroupID, f.users[0].ID, SendInput{Content: "@AI isolated", ClientID: uuid.NewString()}); err != nil {
		t.Fatal(err)
	}
	other, err := f.store.Claim(ctx, time.Minute)
	if err != nil || other.GroupID != *request.GroupID {
		t.Fatal("second group not independently claimable")
	}
	snapshot, _, err := f.store.AgentContext(ctx, other, 8192)
	if err != nil || len(snapshot.Messages) != 1 || strings.Contains(snapshot.Messages[0].Text, "large") {
		t.Fatal("other group context leaked", snapshot, err)
	}
}

func TestPostgresRecentQAContextAndRetry(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	for i := 0; i < 205; i++ {
		_, err := f.service.Send(ctx, f.group, f.users[0].ID, SendInput{Content: strings.Repeat("之前的聊天内容", 80), ClientID: uuid.NewString()})
		if err != nil {
			t.Fatal(err)
		}
	}
	trigger, err := f.service.Send(ctx, f.group, f.users[0].ID, SendInput{Content: "@AI 最近聊了什么？", ClientID: uuid.NewString(), AgentKind: "qa"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Send(ctx, f.group, f.users[1].ID, SendInput{Content: "未来消息不得进入原提问", ClientID: uuid.NewString()}); err != nil {
		t.Fatal(err)
	}
	run, err := f.store.Claim(ctx, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// Reproduce a failed run, then retry the same immutable trigger with a
	// usable budget. The long history must no longer make retry fail forever.
	f.capacity = 100
	f.worker(t).Execute(ctx, run)
	if err := f.store.Retry(ctx, f.group, f.users[0].ID, run.ID); err != nil {
		t.Fatal(err)
	}
	run, err = f.store.Claim(ctx, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	f.capacity = 16384
	calls := 0
	f.model = testModel{func(_ context.Context, request llm.Request) (llm.Result, error) {
		calls++
		var payload struct {
			Context  ai.Context `json:"context"`
			Question string     `json:"question"`
		}
		if err := json.Unmarshal([]byte(request.Messages[1].Content), &payload); err != nil {
			t.Fatal(err)
		}
		messages := payload.Context.Messages
		if len(messages) == 0 || len(messages) > 200 || messages[len(messages)-1].ID != trigger.ID || payload.Question != "最近聊了什么？" {
			t.Fatal("trigger was lost", payload)
		}
		if payload.Context.HistoryScope == nil || !payload.Context.HistoryScope.EarlierMessagesOmitted || strings.Contains(request.Messages[1].Content, "未来消息不得进入") {
			t.Fatal("context coverage or snapshot boundary invalid")
		}
		if len(request.Messages[0].Content)+len(request.Messages[1].Content)+2560 > f.capacity {
			t.Fatal("over-budget request reached model")
		}
		body, _ := json.Marshal(map[string]any{"status": "answered", "answer": "近期聊天", "sourceMessageIds": []string{trigger.ID}})
		return llm.Result{Content: string(body)}, nil
	}}
	f.worker(t).Execute(ctx, run)
	runs, err := f.store.Runs(ctx, f.group, f.users[0].ID)
	if err != nil || calls != 1 || len(runs) != 1 || runs[0].Status != "succeeded" {
		t.Fatal("long-history retry did not recover", runs, err)
	}
	page, err := f.store.Messages(ctx, f.group, f.users[0].ID, trigger.Seq+1, 0, 100)
	if err != nil || len(page.Messages) != 1 || !strings.Contains(page.Messages[0].Content, "未检索更早记录") || page.Messages[0].AgentResult == nil || page.Messages[0].AgentResult.HistoryScope == nil {
		t.Fatal("coverage was not persisted", page, err)
	}
}

func (f *fixture) configureAI(model llm.ChatModel, name string, tokens int, timeout time.Duration, injectors ...aicontext.ContextInjector) {
	f.model = model
	f.modelName = name
	f.capacity = tokens
	f.timeout = timeout
	if len(injectors) > 0 {
		f.injector = injectors[0]
	}
	f.service = NewService(f.store, model != nil)
}
func (f *fixture) worker(t *testing.T) *jobs.Worker {
	t.Helper()
	executor, err := bootstrap.New(f.model, f.modelName, max(4096, f.capacity))
	if err != nil {
		t.Fatal(err)
	}
	return jobs.New(f.store, executor, f.capacity, f.timeout, f.injector)
}
