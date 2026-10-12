package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

type cacheCountingModel struct {
	stubModel
	identity      string
	identityErr   error
	reviewCalls   int
	onReview      func()
	identityCalls int
}

func (m *cacheCountingModel) ReviewCacheIdentity(context.Context, string) (string, error) {
	m.identityCalls++
	return m.identity, m.identityErr
}
func (m *cacheCountingModel) Call(ctx context.Context, r ModelRequest) (ModelResult, error) {
	if strings.HasPrefix(r.Messages[0].Content, actionReviewPrompt) {
		m.reviewCalls++
		if m.onReview != nil {
			defer m.onReview()
		}
	}
	return m.stubModel.Call(ctx, r)
}
func cachedWorkflow(t *testing.T) (*Store, *Workflow, *cacheCountingModel) {
	s, w, _ := setup(t, "")
	m := &cacheCountingModel{identity: "endpoint|model|digest-v1", stubModel: stubModel{content: `{"suggestions":[{"title":"测试任务","assigneeId":"lin","timeExpression":"明天","sourceMessageIds":["m6"]}]}`}}
	w.Model = m
	w.EnableReviewCache = true
	return s, w, m
}

func TestReviewCacheDisabledByDefault(t *testing.T) {
	_, w, m := cachedWorkflow(t)
	w.EnableReviewCache = false
	for i := 0; i < 2; i++ {
		run, err := w.Execute(context.Background(), "daily", "lin", "extract", "", "")
		if err != nil || run.PromptVersion != "extract-v4" || cacheState(run) != "absent" {
			t.Fatalf("normal workflow used experimental cache: %v", err)
		}
	}
	if m.reviewCalls != 2 || m.identityCalls != 0 || len(w.reviewCache.entries) != 0 {
		t.Fatal("disabled cache still queried model digest or reused result")
	}
	_, ordinary, _ := setup(t, "")
	if ordinary.EnableReviewCache {
		t.Fatal("constructor enabled cache by default")
	}
}
func cacheState(run Run) string {
	for _, step := range run.Steps {
		if step.Name == "review_cache" {
			return step.Output.(map[string]any)["status"].(string)
		}
	}
	return "absent"
}
func TestReviewCacheHitKeepsCurrentRunIDs(t *testing.T) {
	_, w, m := cachedWorkflow(t)
	first, err := w.Execute(context.Background(), "daily", "lin", "extract", "", "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := w.Execute(context.Background(), "daily", "lin", "extract", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if m.reviewCalls != 1 || cacheState(first) != "miss" || cacheState(second) != "hit" {
		t.Fatalf("expected one review: %d %s %s", m.reviewCalls, cacheState(first), cacheState(second))
	}
	if first.Suggestions[0].ID == second.Suggestions[0].ID || !strings.HasPrefix(second.Suggestions[0].ID, second.ID) {
		t.Fatal("cached suggestion reused old run identity")
	}
	for _, step := range second.Steps {
		if step.Name == "review_model_call" {
			t.Fatal("hit falsely records a model call")
		}
	}
}
func TestReviewCacheContextCandidateAndModelInvalidation(t *testing.T) {
	s, w, m := cachedWorkflow(t)
	execute := func() {
		t.Helper()
		if _, err := w.Execute(context.Background(), "daily", "lin", "extract", "", ""); err != nil {
			t.Fatal(err)
		}
	}
	execute()
	if _, err := s.AppendMessage("daily", "lin", "补充信息"); err != nil {
		t.Fatal(err)
	}
	execute()
	if m.reviewCalls != 2 {
		t.Fatal("new message did not invalidate cache")
	}
	m.content = strings.ReplaceAll(m.content, "测试任务", "另一任务")
	execute()
	if m.reviewCalls != 3 {
		t.Fatal("candidate did not invalidate cache")
	}
	m.identity = "endpoint|model|digest-v2"
	execute()
	if m.reviewCalls != 4 {
		t.Fatal("same alias with new weights did not invalidate cache")
	}
}
func TestReviewCacheDoesNotCacheFailedOrUnavailableReviews(t *testing.T) {
	for _, body := range []string{`{"reviews":[]}`, `{"reviews":[{"candidateIndex":0,"status":"active","evidenceMessageIds":["m8"],"reason":"跨群"}]}`, `invalid`} {
		_, w, m := cachedWorkflow(t)
		m.reviewContent = body
		for i := 0; i < 2; i++ {
			run, err := w.Execute(context.Background(), "daily", "lin", "extract", "", "")
			if err == nil || len(run.Suggestions) > 0 {
				t.Fatal("bad review exposed candidates")
			}
		}
		if m.reviewCalls != 2 || len(w.reviewCache.entries) != 0 {
			t.Fatal("failed result cached")
		}
	}
	_, w, m := cachedWorkflow(t)
	m.identityErr = errors.New("fingerprint unavailable")
	for i := 0; i < 2; i++ {
		run, err := w.Execute(context.Background(), "daily", "lin", "extract", "", "")
		if err != nil || cacheState(run) != "bypass" {
			t.Fatalf("fingerprint outage must perform review: %v", err)
		}
	}
	if m.reviewCalls != 2 {
		t.Fatal("unidentified model verdict reused")
	}
}

func TestReviewCacheDoesNotFreezeUncertainVerdicts(t *testing.T) {
	_, w, m := cachedWorkflow(t)
	m.reviewContent = `{"reviews":[{"candidateIndex":0,"status":"uncertain","evidenceMessageIds":["m6"],"reason":"状态存在歧义"}]}`
	for i := 0; i < 2; i++ {
		run, err := w.Execute(context.Background(), "daily", "lin", "extract", "", "")
		if err != nil || cacheState(run) != "miss" {
			t.Fatalf("uncertain result reused: %v", err)
		}
	}
	if m.reviewCalls != 2 || len(w.reviewCache.entries) != 0 {
		t.Fatal("uncertainty locked in cache")
	}
}

func TestReviewCacheDoesNotStoreAfterWeightsChangeDuringCall(t *testing.T) {
	_, w, m := cachedWorkflow(t)
	m.onReview = func() { m.identity = "endpoint|model|digest-v2" }
	run, err := w.Execute(context.Background(), "daily", "lin", "extract", "", "")
	if err != nil || cacheState(run) != "miss" || len(w.reviewCache.entries) != 0 {
		t.Fatalf("verdict stored under obsolete weights: %v", err)
	}
}
func TestReviewCacheExpiryAndBound(t *testing.T) {
	_, w, m := cachedWorkflow(t)
	if _, err := w.Execute(context.Background(), "daily", "lin", "extract", "", ""); err != nil {
		t.Fatal(err)
	}
	for key, entry := range w.reviewCache.entries {
		entry.expires = time.Now().Add(-time.Second)
		w.reviewCache.entries[key] = entry
	}
	run, err := w.Execute(context.Background(), "daily", "lin", "extract", "", "")
	if err != nil || m.reviewCalls != 2 || cacheState(run) != "miss" {
		t.Fatal("expired verdict reused")
	}
	var c reviewCache
	for i := 0; i < reviewCacheLimit+1; i++ {
		c.put(time.Unix(int64(i), 0).String(), "value")
	}
	if len(c.entries) != reviewCacheLimit || len(c.order) != reviewCacheLimit {
		t.Fatal("unbounded storage")
	}
	if _, hit := c.get(time.Unix(0, 0).String()); hit {
		t.Fatal("oldest entry not evicted")
	}
}
func TestReviewCacheKeyCoversAllSemanticInputAndConfiguration(t *testing.T) {
	assignee := "lin"
	input := Context{ActorID: "lin", GroupID: "g", Users: []User{{ID: "lin", Nickname: "A"}}, Messages: []Message{{ID: "m", GroupID: "g", SenderID: "lin", Text: "安排"}}, Tasks: []Task{{ID: "t", GroupID: "g", Status: "pending"}}}
	candidates := []Suggestion{{ID: "old-run", Title: "任务", AssigneeID: &assignee, TimeExpression: "明天", SourceMessageIDs: []string{"m"}}}
	request := ModelRequest{Model: "model", Messages: []ChatMessage{{Role: "system", Content: "prompt"}, {Role: "user"}}, Format: json.RawMessage(`{"type":"object"}`), Options: ModelOptions{NumCtx: 8192, NumPredict: 2048}}
	key, err := actionReviewCacheKey("identity", input, candidates, request)
	if err != nil {
		t.Fatal(err)
	}
	changedID := append([]Suggestion(nil), candidates...)
	changedID[0].ID = "new-run"
	same, _ := actionReviewCacheKey("identity", input, changedID, request)
	if same != key {
		t.Fatal("ephemeral ID caused a miss")
	}
	tests := map[string]func(*Context, *[]Suggestion, *ModelRequest){
		"actor":           func(c *Context, _ *[]Suggestion, _ *ModelRequest) { c.ActorID = "other" },
		"group":           func(c *Context, _ *[]Suggestion, _ *ModelRequest) { c.GroupID = "other" },
		"sender":          func(c *Context, _ *[]Suggestion, _ *ModelRequest) { c.Messages[0].SenderID = "other" },
		"timestamp":       func(c *Context, _ *[]Suggestion, _ *ModelRequest) { c.Messages[0].SentAt = time.Unix(1, 0) },
		"members":         func(c *Context, _ *[]Suggestion, _ *ModelRequest) { c.Users[0].Nickname = "changed" },
		"task completion": func(c *Context, _ *[]Suggestion, _ *ModelRequest) { c.Tasks[0].Status = "completed" },
		"assignee":        func(_ *Context, s *[]Suggestion, _ *ModelRequest) { v := "other"; (*s)[0].AssigneeID = &v },
		"time":            func(_ *Context, s *[]Suggestion, _ *ModelRequest) { (*s)[0].TimeExpression = "后天" },
		"source":          func(_ *Context, s *[]Suggestion, _ *ModelRequest) { (*s)[0].SourceMessageIDs = []string{"another"} },
		"candidate order": func(_ *Context, s *[]Suggestion, _ *ModelRequest) { *s = append(*s, Suggestion{Title: "另一个"}) },
		"prompt":          func(_ *Context, _ *[]Suggestion, r *ModelRequest) { r.Messages[0].Content = "new prompt" },
		"schema":          func(_ *Context, _ *[]Suggestion, r *ModelRequest) { r.Format = json.RawMessage(`{"type":"array"}`) },
		"options":         func(_ *Context, _ *[]Suggestion, r *ModelRequest) { r.Options.NumCtx = 16384 },
		"model name":      func(_ *Context, _ *[]Suggestion, r *ModelRequest) { r.Model = "other" },
	}
	for name, modify := range tests {
		t.Run(name, func(t *testing.T) {
			var c Context
			var s []Suggestion
			var r ModelRequest
			b, _ := json.Marshal(input)
			_ = json.Unmarshal(b, &c)
			b, _ = json.Marshal(candidates)
			_ = json.Unmarshal(b, &s)
			b, _ = json.Marshal(request)
			_ = json.Unmarshal(b, &r)
			modify(&c, &s, &r)
			different, err := actionReviewCacheKey("identity", c, s, r)
			if err != nil || different == key {
				t.Fatalf("key not invalidated: %v", err)
			}
		})
	}
}
