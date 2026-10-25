package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/execution"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/jobs"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/llm"
	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
	respondtool "github.com/LizHu95/Jetaime/services/api/internal/ai/tools/respond"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func policyDecision() *ai.Recognition {
	return &ai.Recognition{ShouldIntervene: true, Reason: "计划尚有缺口", Breakdowns: []ai.Breakdown{{Type: "underspecified_plan"}}, Intent: &ai.InterventionIntent{Type: "clarify", Objective: "确认预算"}}
}

func TestInterventionKeyBoundaries(t *testing.T) {
	decision := policyDecision()
	key := interventionKey(" 周末 Budget 500元 ", decision)
	if key == "" || key != interventionKey("周末budget500元", decision) {
		t.Fatal("whitespace/case normalization failed")
	}
	for _, topic := range []string{"周末budget600元", "下周budget500元", "周末budget50.0元", "周末budget-500元"} {
		if key == interventionKey(topic, decision) {
			t.Fatalf("different constraint conflated: %s", topic)
		}
	}
	if interventionKey("10.5", decision) == interventionKey("105", decision) {
		t.Fatal("decimal punctuation lost")
	}
	changed := *decision
	changed.Breakdowns = []ai.Breakdown{{Type: "conflict"}}
	if key == interventionKey("周末budget500元", &changed) {
		t.Fatal("new breakdown suppressed as previous help")
	}
	changed.Intent = &ai.InterventionIntent{Type: "suggest"}
	if interventionKey("同一事项", &changed) == interventionKey("同一事项", decision) {
		t.Fatal("different help intent conflated")
	}
	decision.Breakdowns = []ai.Breakdown{{Type: "uncertainty"}, {Type: "conflict"}}
	changed = *decision
	changed.Breakdowns = []ai.Breakdown{{Type: "conflict"}, {Type: "uncertainty"}}
	if interventionKey("同一事项", &changed) != interventionKey("同一事项", decision) {
		t.Fatal("breakdown ordering changed key")
	}
	for _, decision := range []*ai.Recognition{nil, {}, {ShouldIntervene: true}, {ShouldIntervene: true, Intent: &ai.InterventionIntent{Type: "execute_request"}}} {
		if interventionKey("同一事项", decision) != "" {
			t.Fatal("silent or explicit request recorded as unsolicited help")
		}
	}
	if interventionKey(" \t\n", policyDecision()) != "" {
		t.Fatal("empty topic produced key")
	}
}

func claimPolicyRun(t *testing.T, f fixture, topic string) Run {
	t.Helper()
	ctx := context.Background()
	message, err := f.service.Send(ctx, f.group, f.users[0].ID, SendInput{Content: topic, ClientID: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	dueProactive(t, f)
	run, err := f.store.Claim(ctx, time.Minute)
	if err != nil || run.TriggerMode != "proactive" || run.TriggerID != message.ID {
		t.Fatalf("claim proactive: %+v %v", run, err)
	}
	run.InterventionTopic, run.Recognition = topic, policyDecision()
	run.Recognition.Intent.EvidenceMessageIDs = []string{message.ID}
	return run
}

func enablePolicy(t *testing.T, f fixture) {
	t.Helper()
	if _, err := f.service.UpdateSettings(context.Background(), f.group, f.users[0].ID, true); err != nil {
		t.Fatal(err)
	}
}

func agePolicyRun(t *testing.T, f fixture, run Run, age time.Duration) {
	t.Helper()
	if _, err := f.store.pool.Exec(context.Background(), `UPDATE agent_runs SET finished_at=clock_timestamp()-$2*interval '1 second' WHERE id=$1`, run.ID, int64(age/time.Second)); err != nil {
		t.Fatal(err)
	}
}

func assertPolicyReply(t *testing.T, f fixture, run Run, delivered bool, reason string) {
	t.Helper()
	var response *string
	var recognition *ai.Recognition
	var key *string
	var status string
	err := f.store.pool.QueryRow(context.Background(), `SELECT status,response_message_id,recognition,intervention_key FROM agent_runs WHERE id=$1`, run.ID).Scan(&status, &response, &recognition, &key)
	if err != nil || status != "succeeded" || (response != nil) != delivered {
		t.Fatalf("unexpected completion: status=%s response=%v err=%v", status, response, err)
	}
	if reason != "" && (recognition == nil || recognition.ShouldIntervene || !strings.Contains(recognition.Reason, reason) || key != nil) {
		t.Fatalf("suppression not persisted: recognition=%+v key=%v", recognition, key)
	}
}

func TestPostgresInterventionCooldownAndCrossWindowDedup(t *testing.T) {
	f := setup(t)
	enablePolicy(t, f)
	ctx := context.Background()
	first := claimPolicyRun(t, f, "周末预算500元")
	if reason, err := f.store.CheckIntervention(ctx, first, first.InterventionTopic, *first.Recognition); err != nil || reason != "" {
		t.Fatal("first intervention suppressed", reason, err)
	}
	if err := f.store.Complete(ctx, first, "先确认预算？", ""); err != nil {
		t.Fatal(err)
	}
	var key *string
	if err := f.store.pool.QueryRow(ctx, `SELECT intervention_key FROM agent_runs WHERE id=$1`, first.ID).Scan(&key); err != nil || key == nil {
		t.Fatal("delivered help not recorded", err)
	}
	// A newly constructed Store has no process-local state to reuse.
	restarted := NewStore(f.store.pool)
	second := claimPolicyRun(t, f, "换个话题")
	if reason, err := restarted.CheckIntervention(ctx, second, second.InterventionTopic, *second.Recognition); err != nil || reason != "cooldown" {
		t.Fatal("cooldown lost across store restart", reason, err)
	}
	if err := restarted.Complete(ctx, second, "不应该发出的第二条", ""); err != nil {
		t.Fatal(err)
	}
	assertPolicyReply(t, f, second, false, "cooldown")
	// Age only the delivered reply: the freshly suppressed run must not prolong cooldown.
	agePolicyRun(t, f, first, interventionCooldown+time.Minute)
	for i := 0; i < ai.InterventionMessageLimit+1; i++ {
		if _, err := f.service.Send(ctx, f.group, f.users[i%2].ID, SendInput{Content: fmt.Sprintf("中间的普通讨论%d", i), ClientID: uuid.NewString()}); err != nil {
			t.Fatal(err)
		}
	}
	third := claimPolicyRun(t, f, "周末预算500元")
	input, _, err := restarted.AgentContext(ctx, third, 16384)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range input.Messages {
		if message.ID == first.TriggerID || message.Text == "先确认预算？" {
			t.Fatal("test did not move beyond recognition window")
		}
	}
	if reason, err := restarted.CheckIntervention(ctx, third, third.InterventionTopic, *third.Recognition); err != nil || reason != "duplicate_topic" {
		t.Fatal("cross-window duplicate allowed", reason, err)
	}
	if err := restarted.Complete(ctx, third, "重复确认预算", ""); err != nil {
		t.Fatal(err)
	}
	assertPolicyReply(t, f, third, false, "duplicate_topic")
	// The next assessment remains allowed after cooldown for a different subject.
	fourth := claimPolicyRun(t, f, "下周出行地点")
	if reason, err := restarted.CheckIntervention(ctx, fourth, fourth.InterventionTopic, *fourth.Recognition); err != nil || reason != "" {
		t.Fatal("silent run extended cooldown or different topic blocked", reason, err)
	}
	if err := restarted.Complete(ctx, fourth, "", ""); err != nil {
		t.Fatal(err)
	}
	agePolicyRun(t, f, first, interventionDedupWindow+time.Minute)
	fifth := claimPolicyRun(t, f, "周末预算500元")
	if reason, err := restarted.CheckIntervention(ctx, fifth, fifth.InterventionTopic, *fifth.Recognition); err != nil || reason != "" {
		t.Fatal("expired duplicate retained", reason, err)
	}
	if err := restarted.Complete(ctx, fifth, "过期后可以再次帮助", ""); err != nil {
		t.Fatal(err)
	}
	assertPolicyReply(t, f, fifth, true, "")
}

func TestPostgresInterventionFinalCheckAndExplicitRequests(t *testing.T) {
	f := setup(t)
	enablePolicy(t, f)
	ctx := context.Background()
	first := claimPolicyRun(t, f, "周末预算")
	if err := f.store.Complete(ctx, first, "确认预算", ""); err != nil {
		t.Fatal(err)
	}
	agePolicyRun(t, f, first, interventionCooldown+time.Minute)
	second := claimPolicyRun(t, f, "新的交通安排")
	if reason, err := f.store.CheckIntervention(ctx, second, second.InterventionTopic, *second.Recognition); err != nil || reason != "" {
		t.Fatal(reason, err)
	}
	// Change committed policy state between the early check and completion.
	// The group-locked completion must read the current state, not a cached decision.
	agePolicyRun(t, f, first, 0)
	if err := f.store.Complete(ctx, second, "过时的许可不能发言", ""); err != nil {
		t.Fatal(err)
	}
	assertPolicyReply(t, f, second, false, "cooldown")
	// Explicit operation requests in passive recognition bypass unsolicited-help policy.
	explicit := claimPolicyRun(t, f, "请AI设置提醒")
	explicit.Recognition.Intent.Type = "execute_request"
	if reason, err := f.store.CheckIntervention(ctx, explicit, explicit.InterventionTopic, *explicit.Recognition); err != nil || reason != "" {
		t.Fatal("explicit request suppressed", reason, err)
	}
	if err := f.store.Complete(ctx, explicit, "提醒已设置", ""); err != nil {
		t.Fatal(err)
	}
	assertPolicyReply(t, f, explicit, true, "")
	// @AI also bypasses the policy, even during cooldown.
	if _, err := f.service.Send(ctx, f.group, f.users[0].ID, SendInput{Content: "@AI 请回答", ClientID: uuid.NewString()}); err != nil {
		t.Fatal(err)
	}
	mention, err := f.store.Claim(ctx, time.Minute)
	if err != nil || mention.TriggerMode != "mention" {
		t.Fatal("mention not claimable", err)
	}
	if err := f.store.Complete(ctx, mention, "主动请求的回答", ""); err != nil {
		t.Fatal(err)
	}
	assertPolicyReply(t, f, mention, true, "")
}

func TestPostgresInterventionFailuresAndAuthorization(t *testing.T) {
	f := setup(t)
	enablePolicy(t, f)
	ctx := context.Background()
	failed := claimPolicyRun(t, f, "周末预算")
	if err := f.store.Complete(ctx, failed, "", "model_failed"); err != nil {
		t.Fatal(err)
	}
	run := claimPolicyRun(t, f, "周末预算")
	if reason, err := f.store.CheckIntervention(ctx, run, run.InterventionTopic, *run.Recognition); err != nil || reason != "" {
		t.Fatal("failed run consumed policy state", reason, err)
	}
	for _, mutate := range []func(*Run){
		func(r *Run) { r.RequestedBy = f.users[2].ID },
		func(r *Run) { r.Attempt++ },
		func(r *Run) { r.ContextEnd++ },
		func(r *Run) { r.ID = uuid.NewString() },
	} {
		forged := run
		mutate(&forged)
		if _, err := f.store.CheckIntervention(ctx, forged, run.InterventionTopic, *run.Recognition); !errors.Is(err, ErrConflict) {
			t.Fatal("unauthorized policy read allowed", err)
		}
	}
	if _, err := f.service.Send(ctx, f.group, f.users[1].ID, SendInput{Content: "已经解决", ClientID: uuid.NewString()}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.CheckIntervention(ctx, run, run.InterventionTopic, *run.Recognition); !errors.Is(err, ErrConflict) {
		t.Fatal("superseded job allowed", err)
	}
	if err := f.store.Complete(ctx, run, "不应发出", ""); !errors.Is(err, ErrConflict) {
		t.Fatal("superseded answer committed", err)
	}
	// A cancelled attempt also leaves the policy clear for its replacement.
	dueProactive(t, f)
	next, err := f.store.Claim(ctx, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if reason, err := f.store.CheckIntervention(ctx, next, "周末预算", *policyDecision()); err != nil || reason != "" {
		t.Fatal("cancelled run consumed policy state", reason, err)
	}
	if err := f.store.Complete(ctx, next, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Claim(ctx, time.Minute); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("silence rescheduled old work", err)
	}
}

func TestPostgresInterventionPolicyThroughWorker(t *testing.T) {
	f := setup(t)
	enablePolicy(t, f)
	ctx := context.Background()
	for round := 0; round < 2; round++ {
		run := claimPolicyRun(t, f, "周末预算")
		calls := 0
		model := testModel{func(_ context.Context, _ llm.Request) (llm.Result, error) {
			calls++
			var body []byte
			if calls == 1 {
				decision := ai.InterventionCandidate{TopicContext: &ai.TopicContext{Topic: "周末预算", Continuity: "continuation", MessageIDs: []string{run.TriggerID}, AnchorMessageIDs: []string{run.TriggerID}}, Action: "intervene", Reason: "还需确认预算", Breakdowns: []ai.Breakdown{}, Intent: &ai.InterventionIntent{Type: "clarify", Objective: "确认预算", EvidenceMessageIDs: []string{run.TriggerID}}}
				body, _ = json.Marshal(decision)
			} else {
				body, _ = json.Marshal(map[string]any{"name": "respond", "arguments": ai.Answer{Status: "answered", Answer: "先确认一下预算？", SourceMessageIDs: []string{run.TriggerID}}})
			}
			return llm.Result{Content: string(body)}, nil
		}}
		registry, err := tools.New(respondtool.New())
		if err != nil {
			t.Fatal(err)
		}
		executor, err := execution.New(model, "test", 16384, registry)
		if err != nil {
			t.Fatal(err)
		}
		jobs.New(f.store, executor, 16384, time.Second).Execute(ctx, run)
		if round == 0 {
			assertPolicyReply(t, f, run, true, "")
			if calls != 2 {
				t.Fatal("first help did not run recognition and main", calls)
			}
		} else {
			assertPolicyReply(t, f, run, false, "cooldown")
			if calls != 1 {
				t.Fatal("suppressed help still called main model", calls)
			}
		}
	}
}
