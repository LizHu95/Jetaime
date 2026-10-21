package chat

import (
	"context"
	"testing"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
	"github.com/google/uuid"
)

func TestExternalToolAuthorization(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	if _, err := f.service.Send(ctx, f.group, f.users[0].ID, SendInput{Content: "上海中午吃什么", MentionsAI: true, ClientID: uuid.NewString()}); err != nil {
		t.Fatal(err)
	}
	run, err := f.store.Claim(ctx, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	input, _, err := f.store.AgentContext(ctx, run, 32768)
	if err != nil {
		t.Fatal(err)
	}
	scope := tools.Scope{Context: input, ContextEndSeq: run.ContextEnd}
	if err := f.service.AuthorizeExternalTool(ctx, scope); err != nil {
		t.Fatal(err)
	}
	wrong := scope
	wrong.Context.ActorID = f.users[2].ID
	if err := f.service.AuthorizeExternalTool(ctx, wrong); err == nil {
		t.Fatal("nonmember authorized")
	}
	wrong = scope
	wrong.ContextEndSeq++
	if err := f.service.AuthorizeExternalTool(ctx, wrong); err == nil {
		t.Fatal("wrong fence authorized")
	}
	input.Request.JobAttempt++
	if err := f.service.AuthorizeExternalTool(ctx, scope); err == nil {
		t.Fatal("stale attempt authorized")
	}
	input.Request.JobAttempt--
	if _, err := f.store.pool.Exec(ctx, `UPDATE agent_runs SET lease_expires_at=now()-interval '1 second' WHERE id=$1`, run.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.service.AuthorizeExternalTool(ctx, scope); err == nil {
		t.Fatal("expired lease authorized")
	}
	if _, err := f.store.pool.Exec(ctx, `UPDATE agent_runs SET lease_expires_at=now()+interval '1 minute' WHERE id=$1`, run.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.service.CloseGroup(ctx, f.group, f.users[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := f.service.AuthorizeExternalTool(ctx, scope); err == nil {
		t.Fatal("closed group authorized")
	}
}
