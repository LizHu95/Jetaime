package chat

import (
	"context"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
	"github.com/google/uuid"
)

// Claim locks the group first, as Send/Read/Close do. A live running lease
// excludes the entire group; expired attempts are recovered before new work.
func (s *Store) Claim(ctx context.Context, lease time.Duration) (Run, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Run{}, err
	}
	defer rollback(tx)
	var group string
	err = tx.QueryRow(ctx, `SELECT g.id::text FROM chat_groups g WHERE g.status='active'
 AND EXISTS(SELECT 1 FROM agent_runs r WHERE r.group_id=g.id AND (r.status='queued' OR (r.status='running' AND r.lease_expires_at<=now())))
 AND NOT EXISTS(SELECT 1 FROM agent_runs r WHERE r.group_id=g.id AND r.status='running' AND r.lease_expires_at>now())
 ORDER BY (SELECT min(context_end_seq) FROM agent_runs r WHERE r.group_id=g.id AND r.status IN ('queued','running'))
 FOR UPDATE OF g SKIP LOCKED LIMIT 1`).Scan(&group)
	if err != nil {
		return Run{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE agent_runs SET status='queued',lease_expires_at=NULL WHERE group_id=$1 AND status='running' AND lease_expires_at<=now()`, group); err != nil {
		return Run{}, err
	}
	var id string
	if err = tx.QueryRow(ctx, `SELECT id::text FROM agent_runs WHERE group_id=$1 AND status='queued' ORDER BY context_end_seq,id LIMIT 1 FOR UPDATE`, group).Scan(&id); err != nil {
		return Run{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE agent_runs SET status='running',attempt_count=attempt_count+1,started_at=now(),lease_expires_at=now()+$2*interval '1 second',error_code=NULL WHERE id=$1`, id, int64(lease/time.Second)); err != nil {
		return Run{}, err
	}
	r, err := scanRun(tx.QueryRow(ctx, runSelect+`WHERE id=$1`, id))
	if err != nil {
		return Run{}, err
	}
	if err = notify(ctx, tx, Event{Type: "ai", GroupID: group}); err != nil {
		return Run{}, err
	}
	return r, tx.Commit(ctx)
}

// Complete uses attempt_count as a fencing token. A late worker cannot write
// after recovery, retry, group closure, or a successful earlier completion.
func (s *Store) Complete(ctx context.Context, run Run, answer, code string, result ...*ai.Result) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err = lockGroup(ctx, tx, run.GroupID, run.RequestedBy); err != nil {
		return err
	}
	var valid bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_runs WHERE id=$1 AND status='running' AND attempt_count=$2 AND lease_expires_at>now())`, run.ID, run.Attempt).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return ErrConflict
	}
	var responseID *string
	status := "failed"
	if code == "" {
		status = "succeeded"
		id := uuid.NewString()
		responseID = &id
		var seq int64
		if err = tx.QueryRow(ctx, `UPDATE chat_groups SET last_message_seq=last_message_seq+1,last_message_at=now() WHERE id=$1 RETURNING last_message_seq`, run.GroupID).Scan(&seq); err != nil {
			return err
		}
		responseKind := run.Kind
		if value := firstResult(result); value != nil {
			responseKind = value.Kind
		}
		if _, err = tx.Exec(ctx, `INSERT INTO messages(id,group_id,seq,sender_type,sender_assistant_id,content,reply_to_id,agent_kind,agent_result) VALUES($1,$2,$3,'assistant',$4,$5,$6,$7,$8)`, id, run.GroupID, seq, run.AssistantID, answer, run.TriggerID, responseKind, firstResult(result)); err != nil {
			return err
		}
	}
	var errorCode *string
	if code != "" {
		errorCode = &code
	}
	if _, err = tx.Exec(ctx, `UPDATE agent_runs SET status=$2,response_message_id=$3,error_code=$4,finished_at=now(),lease_expires_at=NULL WHERE id=$1`, run.ID, status, responseID, errorCode); err != nil {
		return err
	}
	eventType := "ai"
	if responseID != nil {
		eventType = "message"
	}
	if err = notify(ctx, tx, Event{Type: eventType, GroupID: run.GroupID}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func firstResult(results []*ai.Result) *ai.Result {
	if len(results) == 0 {
		return nil
	}
	return results[0]
}
