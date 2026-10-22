package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/bootstrap"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/jobs"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
	respondtool "github.com/LizHu95/Jetaime/services/api/internal/ai/tools/respond"
	"github.com/LizHu95/Jetaime/services/api/internal/auth"
	"github.com/LizHu95/Jetaime/services/api/internal/httpapi"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func dueProactive(t *testing.T, f fixture) {
	t.Helper()
	if _, err := f.store.pool.Exec(context.Background(), `UPDATE chat_groups SET proactive_due_at=clock_timestamp()-interval '1 second' WHERE id=$1`, f.group); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresProactiveDebounceWindowAndSilence(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	state, err := f.service.GetState(ctx, f.group, f.users[0].ID)
	if err != nil || state.ProactiveEnabled {
		t.Fatal("proactive must default off", err)
	}
	if _, err = f.service.UpdateSettings(ctx, f.group, f.users[2].ID, true); !errors.Is(err, ErrNotFound) {
		t.Fatal("outsider changed settings", err)
	}
	if _, err = f.service.UpdateSettings(ctx, f.group, f.users[0].ID, true); err != nil {
		t.Fatal(err)
	}
	var last Message
	for i := 0; i < 35; i++ {
		last, err = f.service.Send(ctx, f.group, f.users[i%2].ID, SendInput{Content: fmt.Sprintf("讨论%d", i), ClientID: uuid.NewString()})
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err = f.store.Claim(ctx, time.Minute); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("triggered before five seconds", err)
	}
	dueProactive(t, f)
	// A new human message resets an already elapsed deadline.
	last, err = f.service.Send(ctx, f.group, f.users[1].ID, SendInput{Content: "继续讨论", ClientID: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	var remaining float64
	if err = f.store.pool.QueryRow(ctx, `SELECT extract(epoch FROM proactive_due_at-clock_timestamp()) FROM chat_groups WHERE id=$1`, f.group).Scan(&remaining); err != nil || remaining < 4 || remaining > 5 {
		t.Fatal("deadline not reset", remaining, err)
	}
	if _, err = f.store.Claim(ctx, time.Minute); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("new activity did not debounce", err)
	}
	dueProactive(t, f)
	run, err := f.store.Claim(ctx, time.Minute)
	if err != nil || run.TriggerMode != "proactive" || run.TriggerMentionsAI || run.TriggerID != last.ID {
		t.Fatal("wrong proactive trigger", run, err)
	}
	if _, err = f.store.Claim(ctx, time.Minute); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("concurrent group claim", err)
	}
	input, _, err := f.store.AgentContext(ctx, run, 16384)
	if err != nil || len(input.Messages) != 30 || input.Messages[29].ID != last.ID || input.Messages[0].Text != "讨论6" {
		t.Fatal("wrong thirty-message window", input, err)
	}
	for _, m := range input.Messages {
		if m.SenderType != "user" || m.SenderName == "" {
			t.Fatal("speaker attribution missing")
		}
	}
	f.capacity = 16384
	f.model = testModel{func(_ context.Context, req llm.Request) (llm.Result, error) {
		var in ai.RecognitionInput
		if err := json.Unmarshal([]byte(req.Messages[1].Content), &in); err != nil || len(in.Messages) != 30 || in.CurrentMessageID != last.ID {
			t.Fatal("recognition window changed", err)
		}
		return llm.Result{Content: `{"topicContext":null,"action":"skip","verification":null,"reason":"讨论正常推进","breakdowns":[],"intent":null}`}, nil
	}}
	f.worker(t).Execute(ctx, run)
	runs, err := f.store.Runs(ctx, f.group, f.users[0].ID)
	if err != nil || len(runs) != 1 || runs[0].Status != "succeeded" || runs[0].ResponseID != nil || runs[0].Recognition == nil || runs[0].Recognition.ShouldIntervene {
		t.Fatal("silence not persisted", runs, err)
	}
	if _, err = f.store.Claim(ctx, time.Minute); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("silence re-triggered", err)
	}
	page, err := f.store.Messages(ctx, f.group, f.users[0].ID, 0, 0, 100)
	if err != nil || len(page.Messages) != 36 {
		t.Fatal("silence created message", err)
	}
}

func TestPostgresProactiveSupersededAndMention(t *testing.T) {
	for _, disable := range []bool{false, true} {
		t.Run(fmt.Sprint("disable=", disable), func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			if _, err := f.service.UpdateSettings(ctx, f.group, f.users[0].ID, true); err != nil {
				t.Fatal(err)
			}
			if _, err := f.service.Send(ctx, f.group, f.users[0].ID, SendInput{Content: "普通对话", ClientID: uuid.NewString()}); err != nil {
				t.Fatal(err)
			}
			dueProactive(t, f)
			run, err := f.store.Claim(ctx, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if disable {
				_, err = f.service.UpdateSettings(ctx, f.group, f.users[1].ID, false)
			} else {
				_, err = f.service.Send(ctx, f.group, f.users[1].ID, SendInput{Content: "@AI 帮忙", ClientID: uuid.NewString()})
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = f.store.Complete(ctx, run, "过时的回复", ""); !errors.Is(err, ErrConflict) {
				t.Fatal("stale proactive reply saved", err)
			}
			if disable {
				_, err = f.service.Send(ctx, f.group, f.users[0].ID, SendInput{Content: "@AI 帮忙", ClientID: uuid.NewString()})
				if err != nil {
					t.Fatal(err)
				}
			}
			mention, err := f.store.Claim(ctx, time.Minute)
			if err != nil || mention.TriggerMode != "mention" {
				t.Fatal("mention waited for proactive deadline", mention, err)
			}
			if err = f.store.Complete(ctx, mention, "正常回答", ""); err != nil {
				t.Fatal(err)
			}
			if _, err = f.store.Claim(ctx, time.Minute); !errors.Is(err, pgx.ErrNoRows) {
				t.Fatal("mention triggered duplicate proactive work", err)
			}
		})
	}
}

func TestPostgresProactiveSettingsHTTP(t *testing.T) {
	f := setup(t)
	router := httpapi.NewRouter(httpapi.Handlers{Chat: NewHandler(f.service, auth.NewHandler(f.auth), nil)})
	for _, tc := range []struct {
		body, token string
		status      int
	}{
		{`{"proactiveEnabled":true}`, "", 401},
		{`{"proactiveEnabled":true}`, f.tokens[2], 404},
		{`{}`, f.tokens[0], 400},
		{`{"proactiveEnabled":null}`, f.tokens[0], 400},
		{`{"proactiveEnabled":true,"userId":"spoof"}`, f.tokens[0], 400},
		{`{"proactiveEnabled":true}`, f.tokens[0], 200},
		{`{"proactiveEnabled":false}`, f.tokens[1], 200},
	} {
		r := httptest.NewRequest(http.MethodPost, "/api/chat/groups/"+f.group+"/settings", strings.NewReader(tc.body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+tc.token)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s: status=%d body=%s", tc.body, w.Code, w.Body.String())
		}
	}
}

func TestPostgresProactiveReplyAndConcurrentActivity(t *testing.T) {
	for _, newActivity := range []bool{false, true} {
		t.Run(fmt.Sprint("newActivity=", newActivity), func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			if _, err := f.service.UpdateSettings(ctx, f.group, f.users[0].ID, true); err != nil {
				t.Fatal(err)
			}
			trigger, err := f.service.Send(ctx, f.group, f.users[0].ID, SendInput{Content: "之前说的预算是不是忘了？", ClientID: uuid.NewString()})
			if err != nil {
				t.Fatal(err)
			}
			dueProactive(t, f)
			run, err := f.store.Claim(ctx, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			var latest Message
			model := testModel{func(_ context.Context, req llm.Request) (llm.Result, error) {
				calls++
				if calls == 1 {
					if newActivity {
						latest, err = f.service.Send(ctx, f.group, f.users[1].ID, SendInput{Content: "已经确认过预算了", ClientID: uuid.NewString()})
						if err != nil {
							t.Fatal(err)
						}
					}
					decision := ai.InterventionCandidate{TopicContext: &ai.TopicContext{Topic: "本次预算", Continuity: "continuation", MessageIDs: []string{trigger.ID}, AnchorMessageIDs: []string{trigger.ID}}, Action: "intervene", Reason: "约束需要确认", Breakdowns: []ai.Breakdown{{Type: "uncertainty", EvidenceMessageIDs: []string{trigger.ID}}}, Intent: &ai.InterventionIntent{Type: "clarify", Objective: "确认是否调整预算", EvidenceMessageIDs: []string{trigger.ID}}}
					body, _ := json.Marshal(decision)
					return llm.Result{Content: string(body)}, nil
				}
				body, _ := json.Marshal(map[string]any{"name": "respond", "arguments": ai.Answer{Status: "answered", Answer: "需要先确认是否调整预算。", SourceMessageIDs: []string{trigger.ID}}})
				return llm.Result{Content: string(body)}, nil
			}}
			registry, err := tools.New(respondtool.New())
			if err != nil {
				t.Fatal(err)
			}
			executor, err := bootstrap.New(model, "test", 16384, registry)
			if err != nil {
				t.Fatal(err)
			}
			jobs.New(f.store, executor, 16384, time.Second).Execute(ctx, run)
			runs, err := f.store.Runs(ctx, f.group, f.users[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			if newActivity {
				if runs[0].Status != "cancelled" || runs[0].ResponseID != nil {
					t.Fatal("late response saved", runs)
				}
				dueProactive(t, f)
				next, err := f.store.Claim(ctx, time.Minute)
				if err != nil || next.TriggerID != latest.ID {
					t.Fatal("new activity lost its next assessment", next, err)
				}
			} else {
				if runs[0].Status != "succeeded" || runs[0].ResponseID == nil || runs[0].Recognition == nil {
					t.Fatal("proactive answer not saved", runs)
				}
				if _, err := f.store.Claim(ctx, time.Minute); !errors.Is(err, pgx.ErrNoRows) {
					t.Fatal("AI reply retriggered proactive work", err)
				}
			}
		})
	}
}
