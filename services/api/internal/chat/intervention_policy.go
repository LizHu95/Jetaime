package chat

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/jackc/pgx/v5"
)

const (
	interventionCooldown    = 2 * time.Minute
	interventionDedupWindow = 24 * time.Hour
)

// interventionKey deliberately performs exact normalized matching, not semantic
// similarity. Preserve punctuation and symbols so dates, amounts and distinct plans
// are not silently treated as the same topic. Changed objectives within the
// same topic/breakdown/intent remain suppressed until the window expires.
func interventionKey(topic string, decision *ai.Recognition) string {
	if decision == nil || !decision.ShouldIntervene || decision.Intent == nil || decision.Intent.Type == "execute_request" {
		return ""
	}
	topic = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, topic)
	if topic == "" {
		return ""
	}
	breakdowns := make([]string, 0, len(decision.Breakdowns))
	for _, breakdown := range decision.Breakdowns {
		breakdowns = append(breakdowns, breakdown.Type)
	}
	sort.Strings(breakdowns)
	return fmt.Sprintf("v1:%x", sha256.Sum256([]byte(topic+"\n"+decision.Intent.Type+"\n"+strings.Join(breakdowns, ","))))
}

type interventionReader interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// CheckIntervention avoids generating an unsolicited answer that would already
// be suppressed. Complete repeats this check under the group transaction lock.
func (s *Store) CheckIntervention(ctx context.Context, run ai.Job, topic string, decision ai.Recognition) (string, error) {
	if run.TriggerMode != "proactive" || decision.Intent != nil && decision.Intent.Type == "execute_request" {
		return "", nil
	}
	var allowed bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (
 SELECT 1 FROM chat_groups g
 JOIN group_members m ON m.group_id=g.id AND m.user_id=$2
 JOIN group_assistants a ON a.group_id=g.id AND a.enabled
 JOIN agent_runs r ON r.group_id=g.id
 WHERE g.id=$1 AND g.status='active' AND g.proactive_enabled
 AND r.id=$3 AND r.requested_by=$2 AND r.trigger_mode='proactive'
 AND r.status='running' AND r.attempt_count=$4 AND r.lease_expires_at>now()
 AND r.trigger_message_id=$5 AND r.context_end_seq=$6
 AND NOT EXISTS(SELECT 1 FROM messages newer WHERE newer.group_id=g.id AND newer.sender_type='user' AND newer.seq>$6)
)`, run.GroupID, run.RequestedBy, run.ID, run.Attempt, run.TriggerID, run.ContextEnd).Scan(&allowed)
	if err != nil {
		return "", err
	}
	if !allowed {
		return "", ErrConflict
	}
	return interventionSuppression(ctx, s.pool, run.GroupID, interventionKey(topic, &decision))
}

// Only committed, delivered unsolicited replies consume cooldown/dedup state.
// Silent, failed, cancelled and explicit-request runs never extend the window.
func interventionSuppression(ctx context.Context, db interventionReader, group, key string) (string, error) {
	var reason string
	err := db.QueryRow(ctx, `SELECT CASE
 WHEN EXISTS(SELECT 1 FROM agent_runs WHERE group_id=$1 AND trigger_mode='proactive'
 AND status='succeeded' AND response_message_id IS NOT NULL AND intervention_key IS NOT NULL
 AND finished_at>statement_timestamp()-$3*interval '1 second') THEN 'cooldown'
 WHEN $2<>'' AND EXISTS(SELECT 1 FROM agent_runs WHERE group_id=$1 AND trigger_mode='proactive'
 AND status='succeeded' AND response_message_id IS NOT NULL AND intervention_key IS NOT NULL
 AND intervention_key=$2 AND finished_at>statement_timestamp()-$4*interval '1 second') THEN 'duplicate_topic'
 ELSE '' END`, group, key, int64(interventionCooldown/time.Second), int64(interventionDedupWindow/time.Second)).Scan(&reason)
	return reason, err
}
