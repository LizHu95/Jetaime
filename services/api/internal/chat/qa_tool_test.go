package chat

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/memory"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/qa"
	"github.com/google/uuid"
)

type qaMemoryModel struct{}

func (qaMemoryModel) Extract(context.Context, memory.Batch) ([]memory.Candidate, error) {
	return []memory.Candidate{}, nil
}
func (qaMemoryModel) Reconcile(context.Context, memory.Candidate, []memory.Document) (memory.Decision, error) {
	return memory.Decision{}, nil
}
func (qaMemoryModel) Embed(_ context.Context, text string) ([]float32, error) {
	if strings.Contains(text, "目的地") || strings.Contains(text, "苏州") {
		return []float32{1, 0, 0}, nil
	}
	return []float32{0, 1, 0}, nil
}

func TestPostgresMainQAToolRecallsIndexedHistoryWithOriginalFence(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	old, err := f.service.Send(ctx, f.group, f.users[0].ID, SendInput{Content: "我们确认的目的地是苏州", ClientID: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	for range 60 {
		if _, err = f.service.Send(ctx, f.group, f.users[1].ID, SendInput{Content: "普通闲聊", ClientID: uuid.NewString()}); err != nil {
			t.Fatal(err)
		}
	}
	cfg := memory.Config{Embedding: memory.EmbeddingConfig{BaseURL: "http://unused", Model: "test-embedding", Revision: "v1", Dimension: 3, MaxInputBytes: 24576}, Extraction: llm.Config{Provider: "ollama", BaseURL: "http://unused", Model: "test", ContextTokens: 32768}}
	mem := &memory.Service{Config: cfg, Store: memory.NewStore(f.store.pool, cfg), Extractor: qaMemoryModel{}, Embedder: qaMemoryModel{}}
	if err = mem.Store.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	index := func() {
		t.Helper()
		for {
			processed, e := mem.ProcessOne(ctx, true)
			if e != nil {
				t.Fatal(e)
			}
			if !processed {
				break
			}
		}
		if e := mem.DrainEmbeddings(ctx); e != nil {
			t.Fatal(e)
		}
	}
	index()
	trigger, err := f.service.Send(ctx, f.group, f.users[0].ID, SendInput{Content: "@AI 我们确认的目的地是哪？", ClientID: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.Send(ctx, f.group, f.users[1].ID, SendInput{Content: "目的地修改为future-only-secret", ClientID: uuid.NewString()}); err != nil {
		t.Fatal(err)
	}
	index() // Even indexed post-question facts must not enter a delayed/retried run.
	f.qaContext = qa.HistoryContext(mem, mem.Store)
	calls := 0
	model := testModel{func(_ context.Context, r llm.Request) (llm.Result, error) {
		calls++
		if strings.Contains(r.Messages[1].Content, "future-only-secret") {
			t.Fatal("post-question evidence leaked")
		}
		if strings.Contains(r.Messages[0].Content, "主Agent") {
			if strings.Contains(r.Messages[1].Content, old.ID) {
				t.Fatal("old evidence unexpectedly present in initial window")
			}
			return llm.Result{Content: `{"name":"answer_question","arguments":{"question":"我们确认的目的地是哪？","context":"history"}}`}, nil
		}
		var payload struct {
			Context struct {
				Messages []struct {
					ID string `json:"id"`
				} `json:"messages"`
			} `json:"context"`
		}
		if e := json.Unmarshal([]byte(r.Messages[1].Content), &payload); e != nil {
			t.Fatal(e)
		}
		found := false
		for _, m := range payload.Context.Messages {
			found = found || m.ID == old.ID
		}
		if !found {
			t.Fatal("vector search did not recover old original message")
		}
		return llm.Result{Content: `{"status":"answered","answer":"我们确认去苏州。","sourceMessageIds":["` + old.ID + `"]}`}, nil
	}}
	f.configureAI(model, "test", 16384, 10*time.Second)
	job, err := f.store.Claim(ctx, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if job.ContextEnd != trigger.Seq {
		t.Fatal("invalid original fence")
	}
	f.worker(t).Execute(ctx, job)
	jobs, err := f.store.Runs(ctx, f.group, f.users[0].ID)
	if err != nil || jobs[0].Status != "succeeded" || calls != 2 {
		t.Fatal("question tool failed", jobs, err, calls)
	}
	page, err := f.store.Messages(ctx, f.group, f.users[0].ID, trigger.Seq+1, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	reply := page.Messages[len(page.Messages)-1]
	if reply.AgentResult == nil || reply.AgentResult.Answer.SourceMessageIDs[0] != old.ID || !reply.AgentResult.HistoryScope.HistoricalSearch || strings.Contains(reply.Content, "未检索更早记录") {
		t.Fatal("historical answer metadata lost", reply)
	}
}
