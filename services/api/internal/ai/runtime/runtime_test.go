package runtime_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/runtime"
)

type agent struct {
	id   string
	call func(context.Context, ai.Invocation) (ai.Run, error)
}

func (a agent) ID() string                                                   { return a.id }
func (a agent) Invoke(ctx context.Context, in ai.Invocation) (ai.Run, error) { return a.call(ctx, in) }

func input(group string) ai.Invocation {
	return ai.Invocation{Context: ai.Context{ActorID: "u", GroupID: group, Users: []ai.User{{ID: "u", Nickname: "user"}}, Messages: []ai.Message{{ID: "m", GroupID: group, SenderID: "u", SentAt: time.Now(), Text: "hello"}}}}
}

func TestRegistrationAndExecutionBoundary(t *testing.T) {
	calls := 0
	a := agent{id: "main", call: func(context.Context, ai.Invocation) (ai.Run, error) { calls++; return ai.Run{}, nil }}
	if _, err := runtime.New(a, a); err == nil {
		t.Fatal("duplicate registration accepted")
	}
	if _, err := runtime.New(agent{}); err == nil {
		t.Fatal("empty identity accepted")
	}
	runner, err := runtime.New(a)
	if err != nil {
		t.Fatal(err)
	}
	service, err := ai.NewService(runner)
	if err != nil {
		t.Fatal(err)
	}
	in := input("g")
	in.AgentID = "missing"
	if _, err := service.Execute(context.Background(), in); err == nil {
		t.Fatal("unknown agent accepted")
	}
	in.AgentID = ""
	in.Context.ActorID = "stranger"
	if _, err := service.Execute(context.Background(), in); err == nil {
		t.Fatal("nonmember accepted")
	}
	in = input("g")
	in.Context.Messages[0].GroupID = "other"
	if _, err := service.Execute(context.Background(), in); err == nil {
		t.Fatal("foreign message accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.Execute(ctx, input("g")); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("invalid invocation reached the agent")
	}
	out, err := service.Execute(context.Background(), input("g"))
	if err != nil || calls != 1 || out.AgentID != "main" || out.InvocationID == "" || out.Status != "succeeded" {
		t.Fatalf("default main execution: %+v %v", out, err)
	}
}

func TestConcurrentInvocationsRetainIndependentIdentity(t *testing.T) {
	a := agent{id: "main", call: func(_ context.Context, in ai.Invocation) (ai.Run, error) {
		return ai.Run{ID: "capability-result", GroupID: in.Context.GroupID}, nil
	}}
	runner, _ := runtime.New(a)
	service, _ := ai.NewService(runner)
	var wg sync.WaitGroup
	ids := make(chan string, 2)
	for _, group := range []string{"one", "two"} {
		wg.Add(1)
		go func(group string) {
			defer wg.Done()
			out, err := service.Execute(context.Background(), input(group))
			if err != nil || out.GroupID != group || out.ID != "capability-result" {
				t.Errorf("execution scope or capability ID changed: %+v %v", out, err)
			}
			ids <- out.InvocationID
		}(group)
	}
	wg.Wait()
	first, second := <-ids, <-ids
	if first == second {
		t.Fatal("concurrent invocations shared identity")
	}
}
