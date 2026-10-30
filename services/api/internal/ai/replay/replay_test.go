package replay

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/qa"
	"github.com/LizHu95/Jetaime/services/api/internal/reminder"
)

type modelFunc func(context.Context, llm.Request) (llm.Result, error)

func (f modelFunc) Call(ctx context.Context, r llm.Request) (llm.Result, error) { return f(ctx, r) }
func fixture(t *testing.T) (*Snapshot, Case) {
	t.Helper()
	root := "../../../../../evals/replay"
	snapshot, err := LoadSnapshot(filepath.Join(root, "messages.example.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	suite, err := LoadSuite(filepath.Join(root, "cases.example.json"), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot, suite.Cases[1]
}
func TestSnapshotIsolationAndEvidenceFence(t *testing.T) {
	snapshot, c := fixture(t)
	v, err := snapshot.View(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range v.Messages {
		if strings.Contains(m.Content, "四点") {
			t.Fatal("future leaked")
		}
	}
	if _, err = v.RecentMessages(context.Background(), "other-group", c.ActorID, v.Fence, 30); err == nil {
		t.Fatal("cross group")
	}
	if _, err = v.RecentMessages(context.Background(), c.GroupID, c.ActorID, v.Fence+1, 30); err == nil {
		t.Fatal("raised fence")
	}
	c.RetrievedMessageIDs = []string{snapshot.Messages[2].ID}
	if _, err = snapshot.View(c); err == nil {
		t.Fatal("future retrieval accepted")
	}
	c.RetrievedMessageIDs = nil
	c.Expected.RequiredSourceIDs = []string{snapshot.Messages[2].ID}
	if _, err = snapshot.View(c); err == nil {
		t.Fatal("future reference accepted")
	}
	c.Expected = Expected{}
	c.ActorID = "unknown"
	if _, err = snapshot.View(c); err == nil {
		t.Fatal("unknown member accepted")
	}
}
func TestSnapshotRejectsMismatchAndDuplicates(t *testing.T) {
	snapshot, _ := fixture(t)
	suite := Suite{Version: "v1", SnapshotSHA256: "wrong", Cases: []Case{}}
	path := filepath.Join(t.TempDir(), "cases.json")
	raw, _ := json.Marshal(suite)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSuite(path, snapshot); err == nil {
		t.Fatal("snapshot mismatch")
	}
	path = filepath.Join(t.TempDir(), "messages.jsonl")
	line, _ := json.Marshal(snapshot.Messages[0])
	raw = append(append(append(line, '\n'), line...), '\n')
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSnapshot(path); err == nil {
		t.Fatal("duplicate accepted")
	}
}
func TestFixedRetrievalDoesNotMutateSnapshot(t *testing.T) {
	snapshot, c := fixture(t)
	v, _ := snapshot.View(c)
	scope := tools.Scope{Context: v.Input, ContextEndSeq: v.Fence}
	// Remove the selected source from initial context so retrieval must add it.
	scope.Context.Messages = scope.Context.Messages[len(scope.Context.Messages)-1:]
	got, err := v.QAContext(context.Background(), qa.ContextRequest{Scope: scope})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 3 || len(snapshot.Messages) != 3 {
		t.Fatal("retrieval not fixed or snapshot mutated")
	}
	for _, m := range got.Messages {
		if strings.Contains(m.Text, "四点") {
			t.Fatal("future leaked")
		}
	}
}

// The same scripted task works with eager and deferred production catalogs.
// Discovery is a real Agent/tool round, so its provider usage must also count.
func discoveryFor(req llm.Request, target string) (llm.Result, bool) {
	var schema struct {
		Properties struct {
			Name struct {
				Enum []string `json:"enum"`
			} `json:"name"`
		} `json:"properties"`
	}
	if json.Unmarshal(req.Format, &schema) != nil {
		return llm.Result{}, false
	}
	hasSearch := false
	for _, name := range schema.Properties.Name.Enum {
		if name == target {
			return llm.Result{}, false
		}
		hasSearch = hasSearch || name == "tool_search"
	}
	if !hasSearch {
		return llm.Result{}, false
	}
	raw, _ := json.Marshal(map[string]any{"name": "tool_search", "arguments": map[string]string{"query": target}})
	tokens := 10
	return llm.Result{Content: string(raw), PromptTokens: &tokens, CompletionTokens: &tokens}, true
}

func TestNestedModelUsageIsCountedOnce(t *testing.T) {
	// Even hostile production env values cannot give the replay a DB or map client.
	t.Setenv("JETAIME_DATABASE_URL", "postgres://must-never-connect.invalid/prod")
	t.Setenv("JETAIME_GAODE_API_KEY", "must-never-be-used")
	snapshot, c := fixture(t)
	calls, businessCalls := 0, 0
	ten, twenty := 10, 20
	model := modelFunc(func(_ context.Context, req llm.Request) (llm.Result, error) {
		calls++
		raw, _ := json.Marshal(req)
		if strings.Contains(string(raw), "四点") {
			t.Fatal("future text in model request")
		}
		if found, needed := discoveryFor(req, "summarize_history"); needed {
			return found, nil
		}
		businessCalls++
		if businessCalls == 1 {
			return llm.Result{Content: `{"name":"summarize_history","arguments":{"startTime":"2026-10-29T00:00:00+08:00","endTime":"2026-10-30T00:00:00+08:00"}}`, PromptTokens: &ten, CompletionTokens: &ten}, nil
		}
		return llm.Result{Content: `{"status":"answered","answer":"约好三点出门。","sourceMessageIds":["00000000-0000-4000-8000-000000000002"]}`, PromptTokens: &twenty, CompletionTokens: &ten}, nil
	})
	result, err := Run(context.Background(), snapshot, c, model, "test", 65536, 1)
	if err != nil || result.Status != "succeeded" {
		t.Fatalf("%v %+v", err, result.Answer)
	}
	if businessCalls != 2 || len(result.Metrics.ModelCalls) != calls || result.Metrics.PromptTokens == nil || *result.Metrics.PromptTokens != 20+10*(calls-1) || *result.Metrics.CompletionTokens != 10*calls {
		t.Fatalf("nested usage: %+v", result.Metrics)
	}
	if !result.Scores["answer_terms"] || !result.Scores["required_sources"] {
		t.Fatal(result.Scores)
	}
	if len(result.Metrics.ToolCalls) != calls-1 || result.Metrics.ToolCalls[len(result.Metrics.ToolCalls)-1] != "summarize_history" {
		t.Fatal(result.Metrics.ToolCalls)
	}
}
func TestErrorsKeepResultsAndUnknownUsage(t *testing.T) {
	snapshot, c := fixture(t)
	result, err := Run(context.Background(), snapshot, c, modelFunc(func(context.Context, llm.Request) (llm.Result, error) {
		return llm.Result{}, errors.New("synthetic failure")
	}), "test", 65536, 1)
	if err != nil || result.Status != "failed" || result.Scores["execution_success"] || result.Metrics.PromptTokens != nil || len(result.Metrics.ModelCalls) != 1 {
		t.Fatalf("%v %+v", err, result.Metrics)
	}
	c.Reviewed = false
	scores := Score(c, Result{Status: "succeeded", Answer: &ai.Answer{Answer: "wrong"}})
	if _, ok := scores["answer_terms"]; ok {
		t.Fatal("unreviewed quality scored")
	}
}
func TestMapFixturesExactMatchAndNoFallback(t *testing.T) {
	f := &fixtureCaller{fixtures: []MapFixture{{Tool: "maps_weather", Arguments: json.RawMessage(`{"city":"上海"}`), Response: json.RawMessage(`{"city":"上海","forecasts":[]}`)}}}
	if _, err := f.Call(context.Background(), "maps_weather", map[string]string{"city": "上海"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Call(context.Background(), "maps_weather", map[string]string{"city": "北京"}); err == nil || f.misses != 1 {
		t.Fatal("missing fixture not flagged")
	}
}
func TestReminderSimulationHasNoCrossRunState(t *testing.T) {
	snapshot, c := fixture(t)
	c.Question = "明天上午九点提醒我喝水"
	c.Expected = Expected{}
	v, _ := snapshot.View(c)
	trigger := v.Input.Request.MessageID
	model := modelFunc(func(_ context.Context, req llm.Request) (llm.Result, error) {
		if found, needed := discoveryFor(req, "set_reminder"); needed {
			return found, nil
		}
		raw, _ := json.Marshal(map[string]any{"name": "set_reminder", "arguments": map[string]any{"action": "save", "title": "喝水", "date": "2026-10-31", "time": "09:00", "sourceMessageIds": []string{trigger}}})
		return llm.Result{Content: string(raw)}, nil
	})
	for i := 1; i <= 2; i++ {
		result, err := Run(context.Background(), snapshot, c, model, "test", 65536, i)
		if err != nil || result.Status != "succeeded" || result.SimulatedReminderOperations != 1 || result.Answer == nil || !strings.Contains(result.Answer.Answer, "已创建") {
			t.Fatalf("run %d: %v %+v", i, err, result.Answer)
		}
	}
	// The test double cannot update somebody else's reminder or bypass versions.
	memory := &memoryReminders{group: c.GroupID, actor: c.ActorID, now: c.At}
	scope := reminder.Scope{GroupID: c.GroupID, UserID: c.ActorID}
	result, err := memory.Create(context.Background(), scope, reminder.CreateInput{Title: "喝水", Date: "2026-10-31", Time: "09:00"}, reminder.Operation{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = memory.Update(context.Background(), scope, result.ReminderID, reminder.UpdateInput{Version: 99}, reminder.Operation{}); err == nil {
		t.Fatal("version bypass")
	}
}
func TestCaseDeadline(t *testing.T) {
	snapshot, c := fixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	result, err := Run(ctx, snapshot, c, modelFunc(func(ctx context.Context, _ llm.Request) (llm.Result, error) {
		<-ctx.Done()
		return llm.Result{}, ctx.Err()
	}), "test", 65536, 1)
	if err != nil || result.Status != "failed" {
		t.Fatal("deadline lost", err)
	}
}
