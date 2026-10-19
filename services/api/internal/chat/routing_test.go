package chat

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/llm"
	"github.com/google/uuid"
)

func TestPostgresAutomaticRoutingPersistsChildResult(t *testing.T) {
	for _, scenario := range []struct{ kind, basis string }{{"main", "general"}, {"main", "history"}, {"extract", "none"}} {
		kind, basis := scenario.kind, scenario.basis
		t.Run(kind+"/"+basis, func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			f.capacity = 16384
			for i := 0; i < 15; i++ {
				if _, err := f.service.Send(ctx, f.group, f.users[0].ID, SendInput{Content: "普通对话", ClientID: uuid.NewString()}); err != nil {
					t.Fatal(err)
				}
			}
			trigger, err := f.service.Send(ctx, f.group, f.users[0].ID, SendInput{Content: "@AI 帮忙", ClientID: uuid.NewString()})
			if err != nil || trigger.AgentKind != "auto" {
				t.Fatal("new request did not enter main agent", trigger, err)
			}
			if _, err := f.service.Send(ctx, f.group, f.users[1].ID, SendInput{Content: "future-only-secret", ClientID: uuid.NewString()}); err != nil {
				t.Fatal(err)
			}
			calls := 0
			f.model = testModel{func(_ context.Context, request llm.Request) (llm.Result, error) {
				calls++
				if strings.Contains(request.Messages[1].Content, "future-only-secret") {
					t.Fatal("future context leaked")
				}
				if strings.Contains(request.Messages[0].Content, "主Agent") {
					if kind == "extract" {
						return llm.Result{Content: `{"action":"extract","basis":"none","answer":null}`}, nil
					}
					sources := `[]`
					if basis == "history" {
						sources = `["` + trigger.ID + `"]`
					}
					return llm.Result{Content: `{"action":"answer","basis":"` + basis + `","answer":{"status":"answered","answer":"直接回答","sourceMessageIds":` + sources + `}}`}, nil
				}
				if kind == "extract" {
					return llm.Result{Content: `{"suggestions":[]}`}, nil
				}
				sources := `[]`
				if kind == "qa" {
					sources = `["` + trigger.ID + `"]`
				}
				return llm.Result{Content: `{"status":"answered","answer":"自动分派的回答","sourceMessageIds":` + sources + `}`}, nil
			}}
			run, err := f.store.Claim(ctx, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			f.worker(t).Execute(ctx, run)
			runs, err := f.store.Runs(ctx, f.group, f.users[0].ID)
			expectedCalls := 1
			if kind == "extract" {
				expectedCalls = 2
			}
			if err != nil || len(runs) != 1 || runs[0].Status != "succeeded" || calls != expectedCalls {
				t.Fatal("automatic execution failed", runs, calls, err)
			}
			page, err := f.store.Messages(ctx, f.group, f.users[0].ID, trigger.Seq+1, 0, 100)
			if err != nil || len(page.Messages) != 1 {
				t.Fatal("reply not saved", page, err)
			}
			reply := page.Messages[0]
			if reply.AgentKind != kind || reply.AgentResult == nil || reply.AgentResult.Kind != kind || reply.AgentResult.Route == nil || reply.AgentResult.Route.Kind != kind {
				t.Fatal("child route lost in persistence", reply)
			}
		})
	}
}

func TestPostgresRoutingFailureCanRetrySameRun(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	if _, err := f.service.Send(ctx, f.group, f.users[0].ID, SendInput{Content: "@AI 怎么除味？", ClientID: uuid.NewString()}); err != nil {
		t.Fatal(err)
	}
	run, err := f.store.Claim(ctx, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	f.model = testModel{func(context.Context, llm.Request) (llm.Result, error) {
		return llm.Result{Content: `{"kind":"unsupported"}`}, nil
	}}
	f.worker(t).Execute(ctx, run)
	runs, err := f.store.Runs(ctx, f.group, f.users[0].ID)
	if err != nil || len(runs) != 1 || runs[0].ErrorCode == nil || *runs[0].ErrorCode != "invalid_model_response" {
		t.Fatal("routing failure misclassified", runs, err)
	}
	if err := f.store.Retry(ctx, f.group, f.users[0].ID, run.ID); err != nil {
		t.Fatal(err)
	}
	f.model = testModel{func(_ context.Context, r llm.Request) (llm.Result, error) {
		if strings.Contains(r.Messages[0].Content, "主Agent") {
			return llm.Result{Content: `{"action":"answer","basis":"general","answer":{"status":"answered","answer":"保持通风","sourceMessageIds":[]}}`}, nil
		}
		return llm.Result{Content: `{"status":"answered","answer":"保持通风","sourceMessageIds":[]}`}, nil
	}}
	run, err = f.store.Claim(ctx, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	f.worker(t).Execute(ctx, run)
	runs, err = f.store.Runs(ctx, f.group, f.users[0].ID)
	if err != nil || len(runs) != 1 || runs[0].Status != "succeeded" || runs[0].Attempt != 2 {
		t.Fatal("routing retry failed", runs, err)
	}
}
