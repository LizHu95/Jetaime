package memory

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
	"github.com/LizHu95/Jetaime/services/api/internal/chat"
	"github.com/LizHu95/Jetaime/services/api/internal/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type fakeEmbedding struct {
	calls atomic.Int32
	fail  atomic.Bool
}

func (f *fakeEmbedding) Embed(_ context.Context, _ string) ([]float32, error) {
	f.calls.Add(1)
	if f.fail.Load() {
		return nil, errors.New("offline")
	}
	return []float32{1, 0.5, 0.25}, nil
}

type fakeExtractor struct {
	calls          atomic.Int32
	reconcileCalls atomic.Int32
	extract        func(Batch) []Candidate
	decision       func(Candidate, []Document) Decision
}

func (f *fakeExtractor) Extract(_ context.Context, b Batch) ([]Candidate, error) {
	f.calls.Add(1)
	if f.extract == nil {
		return []Candidate{}, nil
	}
	return f.extract(b), nil
}
func (f *fakeExtractor) Reconcile(_ context.Context, c Candidate, ds []Document) (Decision, error) {
	f.reconcileCalls.Add(1)
	if f.decision != nil {
		return f.decision(c, ds), nil
	}
	return Decision{Action: "ADD", Content: c.Content}, nil
}
func testConfig() Config {
	return Config{Embedding: EmbeddingConfig{BaseURL: "http://unused", Model: "embed-test", Revision: "v1", Dimension: 3, MaxInputBytes: 24576, APIKeyEnv: "TEST_KEY"}, Extraction: llm.Config{Provider: "openai-compatible", Model: "memory-test", BaseURL: "http://unused", OutputMode: "json_object", ContextTokens: 32768, APIKeyEnv: "TEST_KEY"}}
}

type memoryFixture struct {
	pool                        *pgxpool.Pool
	group, alice, bob, outsider string
	service                     *Service
	embed                       *fakeEmbedding
	extract                     *fakeExtractor
	chat                        *chat.Service
}

func fixture(t *testing.T) *memoryFixture {
	t.Helper()
	url := os.Getenv("JETAIME_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set JETAIME_TEST_DATABASE_URL for isolated PostgreSQL + pgvector tests")
	}
	ctx := context.Background()
	admin, err := database.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	schema := "memory_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, e := admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE")
		if e != nil {
			t.Error(e)
		}
		admin.Close()
	})
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err = database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := &memoryFixture{pool: pool, group: uuid.NewString(), alice: uuid.NewString(), bob: uuid.NewString(), outsider: uuid.NewString(), embed: &fakeEmbedding{}, extract: &fakeExtractor{}}
	if f.alice > f.bob {
		f.alice, f.bob = f.bob, f.alice
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rollback(tx)
	for i, id := range []string{f.alice, f.bob, f.outsider} {
		if _, err = tx.Exec(ctx, `INSERT INTO users(id,nickname) VALUES($1,$2)`, id, []string{"Alice", "Bob", "Carol"}[i]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO chat_groups(id,user_low_id,user_high_id) VALUES($1,$2,$3)`, f.group, f.alice, f.bob); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO group_members(group_id,user_id) VALUES($1,$2),($1,$3)`, f.group, f.alice, f.bob); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO group_assistants(id,group_id,agent_key) VALUES($1,$2,'test')`, uuid.NewString(), f.group); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	f.service = &Service{Config: testConfig(), Store: NewStore(pool, testConfig()), Extractor: f.extract, Embedder: f.embed}
	f.chat = chat.NewService(chat.NewStore(pool), false)
	return f
}
func (f *memoryFixture) send(t *testing.T, actor, text string) chat.Message {
	t.Helper()
	m, err := f.chat.Send(context.Background(), f.group, actor, chat.SendInput{Content: text, ClientID: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	return m
}
func (f *memoryFixture) init(t *testing.T) {
	t.Helper()
	if err := f.service.Store.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func (f *memoryFixture) process(t *testing.T) {
	t.Helper()
	ok, err := f.service.ProcessOne(context.Background(), true)
	if err != nil || !ok {
		t.Fatalf("process: %t %v", ok, err)
	}
}
func (f *memoryFixture) scalar(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPostgresRestartSkipsCompletedDataAndOnlyProcessesNewMessages(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	first := f.send(t, f.alice, "我们决定周六去杭州")
	f.extract.extract = func(b Batch) []Candidate {
		return []Candidate{{Kind: "decision", Subject: "shared", EventKey: "weekend-trip", TemporalScope: "event", Content: "周六去杭州", SourceIDs: []string{first.ID}}}
	}
	f.init(t)
	f.process(t)
	if err := f.service.DrainEmbeddings(ctx); err != nil {
		t.Fatal(err)
	}
	calls := f.embed.calls.Load()
	if calls != 2 {
		t.Fatalf("candidate embedding should be reused by document: %d", calls)
	}
	if err := database.Migrate(ctx, f.pool); err != nil {
		t.Fatal(err)
	} // redeployment migration
	restarted := &Service{Config: testConfig(), Store: NewStore(f.pool, testConfig()), Extractor: f.extract, Embedder: f.embed}
	if err := restarted.Store.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	ok, err := restarted.ProcessOne(ctx, true)
	if err != nil || ok {
		t.Fatalf("reprocessed completed messages: %v %v", ok, err)
	}
	if err = restarted.Store.RepairQueue(ctx); err != nil {
		t.Fatal(err)
	}
	if err = restarted.DrainEmbeddings(ctx); err != nil {
		t.Fatal(err)
	}
	if f.extract.calls.Load() != 1 || f.embed.calls.Load() != calls {
		t.Fatal("restart repeated completed model calls")
	}
	result, err := restarted.Search(ctx, f.group, f.alice, SearchInput{Query: "杭州"})
	if err != nil || len(result.Documents) != 2 {
		t.Fatalf("history recall: %+v %v", result, err)
	}
	second := f.send(t, f.bob, "后来改成周日去苏州")
	result, err = restarted.Search(ctx, f.group, f.alice, SearchInput{Query: "苏州", Mode: "keyword"})
	if err != nil || len(result.RecentUnprocessed) != 1 || result.RecentUnprocessed[0].ID != second.ID {
		t.Fatalf("unindexed recent: %+v %v", result, err)
	}
	f.extract.extract = func(b Batch) []Candidate {
		return []Candidate{{Kind: "decision", Subject: "shared", EventKey: "weekend-trip", TemporalScope: "event", Content: "周日去苏州", SourceIDs: []string{second.ID}, ExplicitChange: true}}
	}
	f.extract.decision = func(c Candidate, ds []Document) Decision {
		return Decision{Action: "UPDATE", TargetID: ds[0].ID, Content: c.Content}
	}
	f.process(t)
	if n := f.scalar(t, `SELECT count(*) FROM memory_documents WHERE group_id=$1 AND kind='decision'`, f.group); n != 2 {
		t.Fatalf("missing version history: %d", n)
	}
	if n := f.scalar(t, `SELECT count(*) FROM memory_documents WHERE group_id=$1 AND kind='decision' AND active AND revision=2`, f.group); n != 1 {
		t.Fatal("new decision is not current")
	}
	if _, err = restarted.Search(ctx, f.group, f.outsider, SearchInput{Query: "杭州"}); !errors.Is(err, ErrNotFound) {
		t.Fatal("cross-space query allowed")
	}
	cfg := testConfig()
	cfg.Embedding.Revision = "v2"
	if err = NewStore(f.pool, cfg).Initialize(ctx); !errors.Is(err, ErrPipeline) {
		t.Fatal("model change silently reset or mixed pipeline", err)
	}
}

func TestPostgresExtractionCheckpointLeaseAndEmbeddingRecovery(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	m := f.send(t, f.alice, "喜欢日料")
	f.init(t)
	b, err := f.service.Store.Claim(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.Store.Claim(ctx, true); !errors.Is(err, ErrNotFound) {
		t.Fatal("concurrent claim allowed")
	}
	cs := []Candidate{{Kind: "preference", Subject: f.alice, EventKey: "food", TemporalScope: "enduring", Content: "Alice喜欢日料", SourceIDs: []string{m.ID}}}
	if err = f.service.Store.SaveExtraction(ctx, b, cs); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(ctx, `UPDATE memory_progress SET lease_until=now()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	recovered, err := f.service.Store.Claim(ctx, true)
	if err != nil || !recovered.Extracted {
		t.Fatal("extraction checkpoint lost", err)
	}
	if err = f.service.Store.Complete(ctx, b, nil); !errors.Is(err, ErrLease) {
		t.Fatal("stale worker committed", err)
	}
	f.embed.fail.Store(true)
	if err = f.service.process(ctx, recovered); err == nil {
		t.Fatal("embedding failure ignored")
	}
	if n := f.scalar(t, `SELECT cursor_seq FROM memory_progress WHERE group_id=$1`, f.group); n != 0 {
		t.Fatal("failed batch advanced cursor")
	}
	f.embed.fail.Store(false)
	if _, err = f.pool.Exec(ctx, `UPDATE memory_embeddings SET retry_at=now()`); err != nil {
		t.Fatal(err)
	}
	if err = f.service.process(ctx, recovered); err != nil {
		t.Fatal(err)
	}
	if f.extract.calls.Load() != 0 {
		t.Fatal("persisted extraction repeated after restart")
	}
	// Simulate process loss after claiming an embedding; expired lease is reclaimable.
	j, err := f.service.Store.claimEmbedding(ctx, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(ctx, `UPDATE memory_embeddings SET lease_until=now()-interval '1 second' WHERE content_hash=$1`, j.Hash); err != nil {
		t.Fatal(err)
	}
	next, err := f.service.Store.claimEmbedding(ctx, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.service.Store.finishEmbedding(ctx, j, []float32{1, 2, 3}); !errors.Is(err, ErrLease) {
		t.Fatal("stale embedding completed", err)
	}
	if _, err = f.service.Store.runEmbedding(ctx, f.embed, next); err != nil {
		t.Fatal(err)
	}
	calls := f.embed.calls.Load()
	if err = f.service.DrainEmbeddings(ctx); err != nil {
		t.Fatal(err)
	}
	if f.embed.calls.Load() != calls {
		t.Fatal("ready vector re-embedded")
	}
}

func TestPostgresNoopTemporaryPreferenceDeletionAndClosure(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	first := f.send(t, f.alice, "一直喜欢辣")
	f.extract.extract = func(b Batch) []Candidate {
		return []Candidate{{Kind: "preference", Subject: f.alice, EventKey: "spicy", TemporalScope: "enduring", Content: "Alice长期喜欢辣", SourceIDs: []string{first.ID}}}
	}
	f.init(t)
	f.process(t)
	if err := f.service.DrainEmbeddings(ctx); err != nil {
		t.Fatal(err)
	}
	second := f.send(t, f.alice, "对，我一直都喜欢辣")
	f.extract.extract = func(b Batch) []Candidate {
		return []Candidate{{Kind: "preference", Subject: f.alice, EventKey: "spicy", TemporalScope: "enduring", Content: "Alice长期喜欢辣", SourceIDs: []string{second.ID}}}
	}
	calls := f.embed.calls.Load()
	f.process(t)
	if f.embed.calls.Load() != calls {
		t.Fatal("duplicate memory re-embedded")
	}
	if n := f.scalar(t, `SELECT count(*) FROM memory_documents WHERE kind='preference' AND active`); n != 1 {
		t.Fatal("NOOP duplicated preference")
	}
	third := f.send(t, f.alice, "今天不想吃辣")
	f.extract.extract = func(b Batch) []Candidate {
		return []Candidate{{Kind: "preference", Subject: f.alice, EventKey: "spicy-today", TemporalScope: "temporary", Content: "Alice今天不想吃辣", SourceIDs: []string{third.ID}}}
	}
	f.process(t)
	if n := f.scalar(t, `SELECT count(*) FROM memory_documents WHERE kind='preference' AND active`); n != 2 {
		t.Fatal("temporary state overwrote enduring preference")
	}
	if f.extract.reconcileCalls.Load() != 0 {
		t.Fatal("unrelated temporal scopes reconciled")
	}
	if _, err := f.pool.Exec(ctx, `UPDATE messages SET content='edited' WHERE id=$1`, third.ID); err == nil {
		t.Fatal("edit left stale index")
	}
	if _, err := f.pool.Exec(ctx, `DELETE FROM messages WHERE id=$1`, first.ID); err != nil {
		t.Fatal(err)
	}
	if n := f.scalar(t, `SELECT count(*) FROM memory_documents WHERE kind='preference' AND temporal_scope='enduring'`); n != 0 {
		t.Fatal("deleted evidence still retrievable")
	}
	if err := f.service.DrainEmbeddings(ctx); err != nil {
		t.Fatal(err)
	}
	if err := chat.NewStore(f.pool).Close(ctx, f.group, f.alice); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"memory_documents", "memory_embeddings", "memory_progress", "memory_batches"} {
		if n := f.scalar(t, "SELECT count(*) FROM "+table); n != 0 {
			t.Fatalf("closed group retained %s", table)
		}
	}
	if _, err := f.service.Search(ctx, f.group, f.alice, SearchInput{Query: "喜欢", Mode: "keyword"}); !errors.Is(err, ErrNotFound) {
		t.Fatal("closed group accessible")
	}
}

func TestCandidateAndReconciliationBoundaries(t *testing.T) {
	now := time.Now()
	b := Batch{From: 2, Messages: []Message{{ID: "old", Seq: 2, SenderID: "alice", SentAt: now}, {ID: "new", Seq: 3, SenderID: "alice", SentAt: now}}}
	good := Candidate{Kind: "preference", Subject: "alice", EventKey: "spicy", TemporalScope: "enduring", Content: "喜欢辣", SourceIDs: []string{"new"}}
	if err := validateCandidates(b, []Candidate{good}); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Candidate){func(c *Candidate) { c.SourceIDs = []string{"foreign"} }, func(c *Candidate) { c.SourceIDs = []string{"old"} }, func(c *Candidate) { c.Subject = "other" }, func(c *Candidate) { c.TemporalScope = "temporary"; c.EventKey = "" }} {
		bad := good
		change(&bad)
		if err := validateCandidates(b, []Candidate{bad}); err == nil {
			t.Fatal("invalid evidence accepted")
		}
	}
	target := Document{ID: uuid.NewString(), Kind: good.Kind, Subject: good.Subject, TemporalScope: good.TemporalScope}
	if err := validateDecision(good, Decision{Action: "UPDATE", TargetID: target.ID}, []Document{target}); err == nil {
		t.Fatal("implicit preference overwritten")
	}
	good.ExplicitChange = true
	target.Subject = "bob"
	if err := validateDecision(good, Decision{Action: "UPDATE", TargetID: target.ID}, []Document{target}); err == nil {
		t.Fatal("different person overwritten")
	}
}

func TestEmbeddingAdapterAndPlaceholderConfiguration(t *testing.T) {
	if _, err := LoadConfig("../../config/memory.example.json"); err == nil {
		t.Fatal("placeholder URL accepted")
	}
	cfg := testConfig()
	t.Setenv("TEST_KEY", "")
	if _, err := cfg.Build(); err == nil {
		t.Fatal("startup accepted missing model credentials")
	}

	t.Setenv("MEMORY_TEST_KEY", "test-key")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("embedding protocol mismatch")
		}
		var body map[string]any
		if e := json.NewDecoder(r.Body).Decode(&body); e != nil {
			t.Error(e)
		}
		if body["dimensions"] != float64(3) || body["encoding_format"] != "float" {
			t.Error("embedding dimensions missing")
		}
		_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[1,2,3]}]}`))
	}))
	defer server.Close()
	embed, err := newEmbedding(EmbeddingConfig{BaseURL: server.URL + "/v1", Model: "test", APIKeyEnv: "MEMORY_TEST_KEY", Dimension: 3, MaxInputBytes: 10})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = embed.Embed(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	if _, err = embed.Embed(context.Background(), strings.Repeat("a", 11)); !errors.Is(err, ErrInvalid) {
		t.Fatal("oversized input sent")
	}
}

func TestPostgresWakeThresholdsAndEmptyExtractionAdvance(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	f.init(t) // No initial history to backfill.
	f.send(t, f.alice, "普通闲聊")
	if ok, err := f.service.ProcessOne(ctx, false); err != nil || ok {
		t.Fatal("fresh single message ignored batching thresholds", ok, err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE chat_groups SET last_message_at=now()-interval '6 minutes' WHERE id=$1`, f.group); err != nil {
		t.Fatal(err)
	}
	if ok, err := f.service.ProcessOne(ctx, false); err != nil || !ok {
		t.Fatal("idle trigger failed", ok, err)
	}
	if n := f.scalar(t, `SELECT cursor_seq FROM memory_progress`); n != 1 {
		t.Fatal("empty candidates did not advance cursor")
	}
	for range 15 {
		f.send(t, f.alice, "普通闲聊")
	}
	if ok, err := f.service.ProcessOne(ctx, false); err != nil || !ok {
		t.Fatal("message count trigger failed", ok, err)
	}
	if n := f.scalar(t, `SELECT cursor_seq FROM memory_progress`); n != 16 {
		t.Fatal("message count batch incomplete")
	}
	if f.embed.calls.Load() != 0 {
		t.Fatal("empty memory candidates triggered embedding before history queue")
	}
}

func TestPostgresLongMessagesDoNotStallAndBatchSourcesAreUnique(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	for range 5 {
		f.send(t, f.alice, strings.Repeat("🙂", 4000))
	}
	f.init(t)
	for range 8 {
		ok, err := f.service.ProcessOne(ctx, true)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
	}
	if n := f.scalar(t, `SELECT cursor_seq FROM memory_progress`); n != 5 {
		t.Fatalf("long-message overlap stalled at %d", n)
	}
	if n := f.scalar(t, `SELECT count(*) FROM memory_document_sources`); n != 5 {
		t.Fatalf("history repeated or omitted source messages: %d", n)
	}
	if f.extract.calls.Load() != 5 {
		t.Fatal("unexpected batch boundaries", f.extract.calls.Load())
	}
}

func TestPostgresInvalidationRetainsVersionsAndEvidence(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	first := f.send(t, f.alice, "周六去杭州")
	f.extract.extract = func(b Batch) []Candidate {
		return []Candidate{{Kind: "plan", Subject: "shared", EventKey: "trip", TemporalScope: "event", Content: "周六去杭州", SourceIDs: []string{first.ID}}}
	}
	f.init(t)
	f.process(t)
	second := f.send(t, f.bob, "取消这次杭州旅行")
	f.extract.extract = func(b Batch) []Candidate {
		return []Candidate{{Kind: "plan", Subject: "shared", EventKey: "trip", TemporalScope: "event", Content: "杭州旅行已取消", ExplicitChange: true, SourceIDs: []string{second.ID}}}
	}
	f.extract.decision = func(c Candidate, ds []Document) Decision {
		return Decision{Action: "INVALIDATE", TargetID: ds[0].ID, Content: c.Content}
	}
	f.process(t)
	if n := f.scalar(t, `SELECT count(*) FROM memory_documents WHERE kind='plan' AND active`); n != 0 {
		t.Fatal("cancelled plan active")
	}
	if n := f.scalar(t, `SELECT count(*) FROM memory_documents WHERE kind='plan'`); n != 2 {
		t.Fatal("cancellation erased historical version")
	}
	if _, err := f.service.Search(ctx, f.group, f.alice, SearchInput{Query: "杭州", Kind: "plan", Mode: "keyword"}); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresCompletedReconciliationResumesWithoutModelCalls(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	m := f.send(t, f.alice, "周六去杭州")
	f.init(t)
	b, err := f.service.Store.Claim(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	c := Candidate{Kind: "plan", Subject: "shared", EventKey: "trip", TemporalScope: "event", Content: "周六去杭州", SourceIDs: []string{m.ID}}
	if err = f.service.Store.SaveExtraction(ctx, b, []Candidate{c}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.Store.CachedEmbedding(ctx, f.embed, f.group, c.Content); err != nil {
		t.Fatal(err)
	}
	if err = f.service.Store.SaveMutations(ctx, b, []Mutation{{Candidate: c, Decision: Decision{Action: "ADD", Content: c.Content}}}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(ctx, `UPDATE memory_progress SET lease_until=now()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	f.embed.fail.Store(true) // No model should be contacted while committing completed stages.
	calls := f.embed.calls.Load()
	f.process(t)
	if f.embed.calls.Load() != calls || f.extract.calls.Load() != 0 || f.extract.reconcileCalls.Load() != 0 {
		t.Fatal("completed stage invoked models again")
	}
	if n := f.scalar(t, `SELECT count(*) FROM memory_documents WHERE kind='plan'`); n != 1 {
		t.Fatal("checkpoint was not committed")
	}
}

func TestPostgresFailedCommitRollsBackDocumentsAndCursor(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	f.send(t, f.alice, "喜欢辣")
	f.init(t)
	b, err := f.service.Store.Claim(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.service.Store.SaveExtraction(ctx, b, []Candidate{}); err != nil {
		t.Fatal(err)
	}
	c := Candidate{Kind: "preference", Subject: f.alice, EventKey: "spicy", TemporalScope: "enduring", Content: "喜欢辣", SourceIDs: []string{uuid.NewString()}}
	if err = f.service.Store.Complete(ctx, b, []Mutation{{Candidate: c, Decision: Decision{Action: "ADD", Content: c.Content}}}); err == nil {
		t.Fatal("missing source accepted")
	}
	for _, table := range []string{"memory_documents", "memory_document_sources", "memory_embeddings"} {
		if n := f.scalar(t, "SELECT count(*) FROM "+table); n != 0 {
			t.Fatalf("partial transaction retained %s", table)
		}
	}
	if n := f.scalar(t, `SELECT cursor_seq FROM memory_progress`); n != 0 {
		t.Fatal("failed transaction advanced cursor")
	}
}

type fixedChatModel struct {
	calls   int
	content string
}

func (m *fixedChatModel) Call(context.Context, llm.Request) (llm.Result, error) {
	m.calls++
	return llm.Result{Content: m.content}, nil
}
func TestMemoryModelBudgetGuardAndExplicitEmptyResults(t *testing.T) {
	model := &fixedChatModel{content: `{"candidates":[]}`}
	cfg := testConfig().Extraction
	cfg.ContextTokens = 4096
	extractor := ModelExtractor{Model: model, Config: cfg}
	if _, err := extractor.Extract(context.Background(), Batch{}); !errors.Is(err, llm.ErrContextLimit) {
		t.Fatal("memory model bypassed local context guard", err)
	}
	if model.calls != 0 {
		t.Fatal("oversized request reached model")
	}
	extractor.Config.ContextTokens = 32768
	if cs, err := extractor.Extract(context.Background(), Batch{}); err != nil || cs == nil {
		t.Fatal("explicit empty result rejected", err)
	}
	model.content = `{}`
	if _, err := extractor.Extract(context.Background(), Batch{}); !errors.Is(err, ErrInvalid) {
		t.Fatal("missing candidates silently advanced progress", err)
	}
}

func TestMemoryConfigNeedsNoSwitchAndLoadsWorkingDirectoryDefault(t *testing.T) {
	cfg := testConfig()
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	if err = os.WriteFile("memory.json", data, 0600); err != nil {
		t.Fatal(err)
	}
	actual, err := LoadConfig("")
	if err != nil {
		t.Fatal(err)
	}
	if actual.Embedding.Model != cfg.Embedding.Model || actual.Extraction.Model != cfg.Extraction.Model {
		t.Fatal("default configuration not loaded")
	}
	var legacy map[string]any
	if err = json.Unmarshal(data, &legacy); err != nil {
		t.Fatal(err)
	}
	legacy["enabled"] = false
	data, err = json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile("memory.json", data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadConfig(""); err == nil {
		t.Fatal("removed switch silently disabled memory")
	}
}
