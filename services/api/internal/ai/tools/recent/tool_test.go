package recent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/memory"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
)

type reader struct {
	messages []memory.Message
	calls    int
}

func (r *reader) RecentMessages(_ context.Context, group, actor string, end int64, limit int) ([]memory.Message, error) {
	r.calls++
	if group != "g" || actor != "u" || end != 42 || limit != 30 {
		return nil, errors.New("scope changed")
	}
	return r.messages, nil
}
func input() tools.Scope {
	now := time.Now()
	return tools.Scope{ContextEndSeq: 42, Context: ai.Context{GroupID: "g", ActorID: "u", Users: []ai.User{{ID: "u", Nickname: "用户"}, {ID: "assistant", Nickname: "AI"}}, Messages: []ai.Message{{ID: "current", GroupID: "g", SenderID: "u", Text: "我要去吗", SentAt: now}}, Request: &ai.RequestContext{MessageID: "current"}}}
}
func TestRecentOriginalsPreserveScopeAndExcludeUnreferencedAI(t *testing.T) {
	for _, explicitReply := range []bool{false, true} {
		scope := input()
		id := "old-ai"
		if explicitReply {
			scope.Context.Messages[0].ReplyToID = &id
		}
		r := &reader{messages: []memory.Message{
			{ID: "trip", Seq: 10, SenderID: "u", SenderType: "user", Text: "下周去成都出差", SentAt: time.Now().Add(-time.Hour)},
			{ID: id, Seq: 11, SenderID: "assistant", SenderType: "assistant", Text: "之前答错的餐厅建议", SentAt: time.Now().Add(-time.Minute)},
		}}
		result, err := New(r).Invoke(context.Background(), scope, json.RawMessage(`{}`))
		if err != nil || result.Terminal || r.calls != 1 {
			t.Fatal("recent read failed", err)
		}
		want := 1
		if explicitReply {
			want = 2
		}
		if len(result.Completion.Sources) != want || result.Completion.Sources[0].ID != "trip" {
			t.Fatal("lost original or repeated stale AI", result.Completion.Sources)
		}
	}
}
func TestRecentOriginalsRequireFenceAndRejectFuture(t *testing.T) {
	scope := input()
	r := &reader{}
	scope.ContextEndSeq = 0
	if _, err := New(r).Invoke(context.Background(), scope, json.RawMessage(`{}`)); err == nil || r.calls != 0 {
		t.Fatal("read without sequence fence")
	}
	scope = input()
	r.messages = []memory.Message{{ID: "future", Seq: 43}}
	if _, err := New(r).Invoke(context.Background(), scope, json.RawMessage(`{}`)); !errors.Is(err, ai.ErrInvalidDecision) {
		t.Fatal("future evidence accepted", err)
	}
}
