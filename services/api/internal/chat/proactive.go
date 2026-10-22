package chat

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *Service) UpdateSettings(ctx context.Context, group, actor string, enabled bool) (State, error) {
	if err := s.store.updateSettings(ctx, group, actor, enabled); err != nil {
		return State{}, err
	}
	return s.GetState(ctx, group, actor)
}

func (s *Store) updateSettings(ctx context.Context, group, actor string, enabled bool) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err = lockGroup(ctx, tx, group, actor); err != nil {
		return err
	}
	// Enabling applies to subsequent messages; toggling cannot replay old chat.
	if _, err = tx.Exec(ctx, `UPDATE chat_groups SET proactive_enabled=$2,proactive_due_at=NULL WHERE id=$1 AND proactive_enabled IS DISTINCT FROM $2`, group, enabled); err != nil {
		return err
	}
	if !enabled {
		if err = cancelProactive(ctx, tx, group, "proactive_disabled"); err != nil {
			return err
		}
	}
	if err = notify(ctx, tx, Event{Type: "ai", GroupID: group}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func cancelProactive(ctx context.Context, tx pgx.Tx, group, reason string) error {
	_, err := tx.Exec(ctx, `UPDATE agent_runs SET status='cancelled',finished_at=now(),lease_expires_at=NULL,error_code=$2 WHERE group_id=$1 AND trigger_mode='proactive' AND status IN ('queued','running')`, group, reason)
	return err
}

// Called while Claim holds the group lock. The unique trigger ID and clearing
// the deadline make both silence and failures durable, without repeated polling calls.
func enqueueProactive(ctx context.Context, tx pgx.Tx, group string) error {
	_, err := tx.Exec(ctx, `INSERT INTO agent_runs(id,group_id,assistant_id,trigger_message_id,requested_by,context_end_seq,agent_kind,trigger_mentions_ai,trigger_mode)
 SELECT $2,g.id,a.id,m.id,m.sender_user_id,m.seq,'auto',false,'proactive'
 FROM chat_groups g JOIN group_assistants a ON a.group_id=g.id AND a.enabled
 JOIN messages m ON m.id=g.proactive_message_id AND m.group_id=g.id
 WHERE g.id=$1 AND g.proactive_enabled AND g.proactive_due_at<=clock_timestamp()
 AND m.sender_type='user' AND NOT m.mentions_ai
 AND NOT EXISTS(SELECT 1 FROM agent_runs r WHERE r.group_id=g.id AND r.status IN ('queued','running'))
 ON CONFLICT(trigger_message_id) DO NOTHING`, group, uuid.NewString())
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE chat_groups SET proactive_due_at=NULL WHERE id=$1 AND NOT EXISTS(SELECT 1 FROM agent_runs WHERE group_id=$1 AND trigger_mode<>'proactive' AND status IN ('queued','running'))`, group)
	return err
}
