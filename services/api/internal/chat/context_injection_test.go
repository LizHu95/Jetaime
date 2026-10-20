package chat

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	aicontext "github.com/LizHu95/Jetaime/services/api/internal/ai/context"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
	"github.com/google/uuid"
)

func TestPostgresDefaultContextFunctionAndDirectReference(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	reference, err := f.service.Send(ctx, f.group, f.users[1].ID, SendInput{Content: "需要直接参考的旧原文", ClientID: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		if _, err := f.service.Send(ctx, f.group, f.users[0].ID, SendInput{Content: "普通聊天", ClientID: uuid.NewString()}); err != nil {
			t.Fatal(err)
		}
	}
	trigger, err := f.service.Send(ctx, f.group, f.users[0].ID, SendInput{Content: "@AI 给个建议", ClientID: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	// Seed the existing reply relation; this test does not introduce a reply UI.
	if _, err := f.store.pool.Exec(ctx, `UPDATE messages SET reply_to_id=$2 WHERE id=$1`, trigger.ID, reference.ID); err != nil {
		t.Fatal(err)
	}
	future, err := f.service.Send(ctx, f.group, f.users[1].ID, SendInput{Content: "不得注入原任务的未来消息", ClientID: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	injections, calls := 0, 0
	inject := func(ctx context.Context, request aicontext.ContextRequest) (ai.Context, error) {
		injections++
		if request.TriggerID != trigger.ID || request.Now.IsZero() || len(request.References) != 1 || request.References[0].ID != reference.ID {
			t.Fatal("injector input incomplete", request.TriggerID)
		}
		return aicontext.InjectDefaultContext(ctx, request)
	}
	model := testModel{func(_ context.Context, request llm.Request) (llm.Result, error) {
		calls++
		var payload struct {
			Context ai.Context `json:"context"`
		}
		if err := json.Unmarshal([]byte(request.Messages[1].Content), &payload); err != nil {
			t.Fatal(err)
		}
		input := payload.Context
		if input.Request == nil || input.Request.MessageID != trigger.ID || len(input.Messages) > aicontext.DefaultRecentMessages+2 || input.Messages[0].ID != reference.ID {
			t.Fatal("injected context lost before model", input.Request)
		}
		if strings.Contains(request.Messages[1].Content, future.Content) {
			t.Fatal("future history leaked")
		}
		if strings.Contains(request.Messages[0].Content, "主Agent") {
			return llm.Result{Content: `{"name":"answer_question","arguments":{"question":"给个建议","context":"mixed"}}`}, nil
		}
		return llm.Result{Content: `{"status":"answered","answer":"建议","sourceMessageIds":["` + reference.ID + `"]}`}, nil
	}}
	f.configureAI(model, "test", 16384, time.Second, inject)
	run, err := f.store.Claim(ctx, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	f.worker(t).Execute(ctx, run)
	runs, err := f.store.Runs(ctx, f.group, f.users[0].ID)
	if err != nil || len(runs) != 1 || runs[0].Status != "succeeded" || injections != 1 || calls != 2 {
		t.Fatal("function injection was not used end to end", runs, injections, calls, err)
	}
	// A future provider may load more history, but cannot cross the trigger fence.
	_, _, err = f.store.AgentContext(ctx, run, 16384, func(ctx context.Context, request aicontext.ContextRequest) (ai.Context, error) {
		input, err := aicontext.InjectDefaultContext(ctx, request)
		if err != nil {
			return input, err
		}
		input.Messages = append(input.Messages, ai.Message{ID: future.ID, GroupID: future.GroupID, SenderID: f.users[1].ID, SentAt: future.CreatedAt, Text: future.Content})
		return input, nil
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatal("custom provider bypassed trigger boundary", err)
	}
}
