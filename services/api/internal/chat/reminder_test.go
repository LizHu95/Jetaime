package chat

import (
	"context"
	"encoding/json"
	"errors"
	remindertool "github.com/LizHu95/Jetaime/services/api/internal/ai/tools/reminder"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/bootstrap"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/jobs"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools/qa"
	"github.com/LizHu95/Jetaime/services/api/internal/auth"
	"github.com/LizHu95/Jetaime/services/api/internal/httpapi"
	"github.com/LizHu95/Jetaime/services/api/internal/reminder"
	"github.com/google/uuid"
)

func TestReminderClarificationAndDurableDelivery(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	store := reminder.NewService(reminder.NewStore(f.store.pool))
	adapter := remindertool.New(store, ReminderOperation)
	delivery := reminder.NewWorker(store)
	scopeFor := func(actor string) reminder.Scope { return reminder.Scope{GroupID: f.group, UserID: actor} }
	apply := func(scope tools.Scope, args remindertool.Arguments) (tools.Result, error) {
		if args.SourceMessageIDs == nil {
			args.SourceMessageIDs = []string{scope.Context.Request.MessageID}
		}
		raw, err := json.Marshal(args)
		if err != nil {
			return tools.Result{}, err
		}
		return adapter.Invoke(ctx, scope, raw)
	}
	tomorrow := time.Now().In(time.FixedZone("Asia/Shanghai", 8*3600)).AddDate(0, 0, 1).Format("2006-01-02")
	model := testModel{call: func(_ context.Context, req llm.Request) (llm.Result, error) {
		var payload struct {
			Context  ai.Context `json:"context"`
			Question string     `json:"question"`
		}
		if err := json.Unmarshal([]byte(req.Messages[1].Content), &payload); err != nil {
			return llm.Result{}, err
		}
		args := remindertool.Arguments{Action: "save", Title: "吃饭", SourceMessageIDs: []string{payload.Context.Request.MessageID}}
		switch payload.Question {
		case "提醒我吃饭":
		case "明天":
			if payload.Context.ReminderDraft == nil {
				t.Fatal("draft missing from context")
			}
			args.Date = tomorrow
			if len(req.Messages) == 2 {
				args.Time = "09:00"
			} // First attempt must be rejected, then repaired.
		case "中午12点":
			if payload.Context.ReminderDraft == nil || payload.Context.ReminderDraft.Date != tomorrow {
				t.Fatal("date lost")
			}
			args.Date = payload.Context.ReminderDraft.Date
			args.Time = "12:00"
		default:
			t.Fatalf("unexpected question %q", payload.Question)
		}
		raw, err := json.Marshal(map[string]any{"name": "set_reminder", "arguments": args})
		return llm.Result{Content: string(raw)}, err
	}}
	registry, err := tools.New(qa.New(model, "test", 16384, nil), adapter)
	if err != nil {
		t.Fatal(err)
	}
	executor, err := bootstrap.New(model, "test", 16384, registry)
	if err != nil {
		t.Fatal(err)
	}
	worker := jobs.New(f.store, executor, 16384, 5*time.Second, adapter.Inject)
	process := func(text string, mention bool) ai.Job {
		t.Helper()
		input := SendInput{Content: text, ClientID: uuid.NewString(), MentionsAI: mention}
		m, err := f.service.Send(ctx, f.group, f.users[0].ID, input)
		if err != nil {
			t.Fatal(err)
		}
		run, err := f.store.Claim(ctx, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if run.TriggerID != m.ID {
			t.Fatal("wrong trigger")
		}
		worker.Execute(ctx, run)
		runs, err := f.store.Runs(ctx, f.group, f.users[0].ID)
		if err != nil || runs[0].Status != "succeeded" {
			t.Fatal("AI failed", runs, err)
		}
		again, err := f.service.Send(ctx, f.group, f.users[0].ID, input)
		if err != nil || again.ID != m.ID {
			t.Fatal("send retry", err)
		}
		return run
	}
	process("提醒我吃饭", true)
	list, err := store.List(ctx, scopeFor(f.users[0].ID))
	if err != nil || len(list) != 0 {
		t.Fatal("created before time clarification", list, err)
	}
	// Other members' ordinary replies cannot complete the requester's draft.
	if _, err = f.service.Send(ctx, f.group, f.users[1].ID, SendInput{Content: "明天", ClientID: uuid.NewString()}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.Claim(ctx, time.Minute); err == nil {
		t.Fatal("peer reply invoked AI")
	}
	process("明天", false)
	list, err = store.List(ctx, scopeFor(f.users[0].ID))
	if err != nil || len(list) != 0 {
		t.Fatal("tomorrow silently received a default time", list, err)
	}
	last := process("中午12点", false)
	list, err = store.List(ctx, scopeFor(f.users[0].ID))
	if err != nil || len(list) != 1 || list[0].Status != "pending" || list[0].DueAt.In(time.FixedZone("Asia/Shanghai", 8*3600)).Format("15:04") != "12:00" {
		t.Fatal(list, err)
	}
	// A stale attempt cannot change reminder state after completion.
	input, question, err := f.store.AgentContext(ctx, last, 16384, adapter.Inject)
	if err != nil {
		t.Fatal(err)
	}
	_, err = apply(tools.Scope{Context: input, Question: question, ContextEndSeq: last.ContextEnd}, remindertool.Arguments{Action: "cancel"})
	if !errors.Is(err, ai.ErrConflict) {
		t.Fatal("stale operation accepted", err)
	}
	if _, err = store.List(ctx, scopeFor(f.users[2].ID)); !errors.Is(err, reminder.ErrNotFound) {
		t.Fatal("list leaked", err)
	}
	if err = store.Cancel(ctx, scopeFor(f.users[1].ID), list[0].ID); !errors.Is(err, reminder.ErrNotFound) {
		t.Fatal("peer could cancel", err)
	}
	// Force the due time rather than sleeping. Independent workers race safely.
	if _, err = f.store.pool.Exec(ctx, `UPDATE reminders SET due_at=now()-interval '1 second' WHERE id=$1`, list[0].ID); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := reminder.NewWorker(reminder.NewService(reminder.NewStore(f.store.pool))).Deliver(ctx)
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if sent, err := delivery.Deliver(ctx); err != nil || sent {
		t.Fatal("duplicate delivery", sent, err)
	}
	list, err = store.List(ctx, scopeFor(f.users[0].ID))
	if err != nil || list[0].Status != "sent" {
		t.Fatal(list, err)
	}
	page, err := f.store.Messages(ctx, f.group, f.users[0].ID, 0, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, message := range page.Messages {
		if strings.HasPrefix(message.Content, "⏰") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("delivered %d reminders", count)
	}
}

func TestReminderRecoveryCancellationAndClosedGroup(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	store := reminder.NewService(reminder.NewStore(f.store.pool))
	adapter := remindertool.New(store, ReminderOperation)
	delivery := reminder.NewWorker(store)
	scopeFor := func(actor string) reminder.Scope { return reminder.Scope{GroupID: f.group, UserID: actor} }
	apply := func(scope tools.Scope, args remindertool.Arguments) (tools.Result, error) {
		if args.SourceMessageIDs == nil {
			args.SourceMessageIDs = []string{scope.Context.Request.MessageID}
		}
		raw, err := json.Marshal(args)
		if err != nil {
			return tools.Result{}, err
		}
		return adapter.Invoke(ctx, scope, raw)
	}
	due := time.Now().In(time.FixedZone("Asia/Shanghai", 8*3600)).AddDate(0, 0, 1)
	m, err := f.service.Send(ctx, f.group, f.users[0].ID, SendInput{Content: "明天中午12点提醒我吃饭", ClientID: uuid.NewString(), MentionsAI: true})
	if err != nil {
		t.Fatal(err)
	}
	run, err := f.store.Claim(ctx, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	input, question, err := f.store.AgentContext(ctx, run, 8192, adapter.Inject)
	if err != nil {
		t.Fatal(err)
	}
	scope := tools.Scope{Context: input, Question: question, ContextEndSeq: run.ContextEnd}
	args := remindertool.Arguments{Action: "save", Title: "吃饭", Date: due.Format("2006-01-02"), Time: "12:00"}
	first, err := apply(scope, args)
	if err != nil {
		t.Fatal(err)
	}
	// A retry of the same tool cannot mutate an already committed operation.
	again, err := apply(scope, remindertool.Arguments{Action: "cancel"})
	if err != nil || !sameReminderResult(first.Data, again.Data) {
		t.Fatal("operation not idempotent", first, again, err)
	}
	if _, err = f.store.pool.Exec(ctx, `UPDATE agent_runs SET lease_expires_at=now()-interval '1 second' WHERE id=$1`, run.ID); err != nil {
		t.Fatal(err)
	}
	recovered, err := f.store.Claim(ctx, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.TriggerID != m.ID || recovered.Attempt != run.Attempt+1 {
		t.Fatal("not recovered", recovered)
	}
	if _, err = apply(scope, args); !errors.Is(err, ai.ErrConflict) {
		t.Fatal("old attempt could write", err)
	}
	input, question, err = f.store.AgentContext(ctx, recovered, 8192, adapter.Inject)
	if err != nil {
		t.Fatal(err)
	}
	again, err = apply(tools.Scope{Context: input, Question: question, ContextEndSeq: recovered.ContextEnd}, args)
	if err != nil || !sameReminderResult(again.Data, first.Data) {
		t.Fatal("recovery changed outcome", err)
	}
	list, err := store.List(ctx, scopeFor(f.users[0].ID))
	if err != nil || len(list) != 1 {
		t.Fatal("duplicate creation", list, err)
	}
	if err = store.Cancel(ctx, scopeFor(f.users[0].ID), list[0].ID); err != nil {
		t.Fatal(err)
	}
	if err = store.Cancel(ctx, scopeFor(f.users[0].ID), list[0].ID); err != nil {
		t.Fatal("cancel not idempotent", err)
	}
	if _, err = f.store.pool.Exec(ctx, `UPDATE reminders SET due_at=now()-interval '1 second' WHERE id=$1`, list[0].ID); err != nil {
		t.Fatal(err)
	}
	if sent, err := delivery.Deliver(ctx); err != nil || sent {
		t.Fatal("cancelled reminder delivered", err)
	}
	if err = f.store.Close(ctx, f.group, f.users[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.List(ctx, scopeFor(f.users[0].ID)); !errors.Is(err, reminder.ErrNotFound) {
		t.Fatal("closed group readable", err)
	}
	if _, err = apply(scope, args); !errors.Is(err, ai.ErrNotFound) {
		t.Fatal("closed group writable", err)
	}
}

func sameReminderResult(a, b any) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return string(left) == string(right)
}

func TestReminderBusinessAPIWithoutAI(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	service := reminder.NewService(reminder.NewStore(f.store.pool))
	router := httpapi.NewRouter(httpapi.Handlers{Reminders: reminder.NewHandler(service, auth.NewHandler(f.auth))})
	base := "/api/chat/groups/" + f.group + "/reminders"
	call := func(method, path string, actor int, body any, want int) reminder.Result {
		t.Helper()
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
		req.Header.Set("Content-Type", "application/json")
		if actor >= 0 {
			req.Header.Set("Authorization", "Bearer "+f.tokens[actor])
		}
		out := httptest.NewRecorder()
		router.ServeHTTP(out, req)
		if out.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, out.Code, out.Body.String())
		}
		var result reminder.Result
		if want == 200 {
			if err := json.Unmarshal(out.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
		}
		return result
	}
	op := uuid.NewString()
	first := call("POST", base, 0, map[string]any{"operationId": op, "title": "吃饭"}, 200)
	if first.Status != "needs_clarification" || first.ReminderID == "" || first.Reminder.Status != reminder.Draft || len(first.MissingFields) != 2 {
		t.Fatal(first)
	}
	again := call("POST", base, 0, map[string]any{"operationId": op, "title": "吃饭"}, 200)
	if !sameReminderResult(first, again) {
		t.Fatal("create retry changed result")
	}
	path := base + "/" + first.ReminderID
	tomorrow := time.Now().In(time.FixedZone("Asia/Shanghai", 8*3600)).AddDate(0, 0, 1).Format("2006-01-02")
	dateOnly := call("PATCH", path, 0, map[string]any{"operationId": uuid.NewString(), "version": first.Reminder.Version, "date": tomorrow}, 200)
	if dateOnly.ReminderID != first.ReminderID || dateOnly.Reminder.DueAt != nil || len(dateOnly.MissingFields) != 1 || dateOnly.MissingFields[0] != "time" {
		t.Fatal(dateOnly)
	}
	call("PATCH", path, 0, map[string]any{"operationId": uuid.NewString(), "version": first.Reminder.Version, "time": "12:00"}, 409)
	call("PATCH", path, 1, map[string]any{"operationId": uuid.NewString(), "version": dateOnly.Reminder.Version, "time": "12:00"}, 404)
	call("PATCH", path, 0, map[string]any{"operationId": uuid.NewString(), "version": dateOnly.Reminder.Version, "status": "sent"}, 400)
	ready := call("PATCH", path, 0, map[string]any{"operationId": uuid.NewString(), "version": dateOnly.Reminder.Version, "time": "12:00"}, 200)
	if ready.ReminderID != first.ReminderID || ready.Status != "created" || ready.Reminder.Status != reminder.Pending {
		t.Fatal(ready)
	}
	// Replaying an old request returns its old result without regressing state.
	call("POST", base, 0, map[string]any{"operationId": op, "title": "吃饭"}, 200)
	list, err := service.List(ctx, reminder.Scope{GroupID: f.group, UserID: f.users[0].ID})
	if err != nil || len(list) != 1 || list[0].Status != reminder.Pending {
		t.Fatal(list, err)
	}
	call("PATCH", path, 0, map[string]any{"operationId": uuid.NewString(), "version": ready.Reminder.Version, "date": ""}, 400)
	cancelled := call("PATCH", path, 0, map[string]any{"operationId": uuid.NewString(), "version": ready.Reminder.Version, "status": "cancelled"}, 200)
	if cancelled.Reminder.Status != reminder.Cancelled {
		t.Fatal(cancelled)
	}
	call("POST", base, -1, map[string]any{"operationId": uuid.NewString(), "title": "吃饭"}, 401)
	call("POST", base, 2, map[string]any{"operationId": uuid.NewString(), "title": "吃饭"}, 404)
	call("POST", base, 1, map[string]any{"operationId": op, "title": "吃饭"}, 409)
	otherDraft := call("POST", base, 0, map[string]any{"operationId": uuid.NewString(), "title": "喝水"}, 200)
	if otherDraft.Reminder.Status != reminder.Draft {
		t.Fatal(otherDraft)
	}
	// An unfinished draft does not block creating a separate, complete reminder.
	otherPending := call("POST", base, 0, map[string]any{"operationId": uuid.NewString(), "title": "散步", "date": tomorrow, "time": "18:00"}, 200)
	if otherPending.Reminder.Status != reminder.Pending {
		t.Fatal(otherPending)
	}
	draftCancelled, err := service.CancelDraft(ctx, reminder.Scope{GroupID: f.group, UserID: f.users[0].ID}, reminder.Operation{ID: uuid.NewString()})
	if err != nil || draftCancelled.ReminderID != otherDraft.ReminderID || draftCancelled.Reminder.DueAt != nil || draftCancelled.Reminder.Status != reminder.Cancelled {
		t.Fatal(draftCancelled, err)
	}
	var count int
	if err = f.store.pool.QueryRow(ctx, `SELECT count(*) FROM agent_runs WHERE group_id=$1`, f.group).Scan(&count); err != nil || count != 0 {
		t.Fatal("business API required AI", count, err)
	}
	if _, err = f.store.Claim(ctx, time.Minute); err == nil {
		t.Fatal("business writes invoked AI")
	}
}
