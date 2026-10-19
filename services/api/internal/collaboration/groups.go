package collaboration

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/LizHu95/Jetaime/services/api/internal/notification"
	"github.com/jackc/pgx/v5"
)

func (s *PostgresStore) Close(ctx context.Context, group, actor string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	var id string
	err = tx.QueryRow(ctx, `SELECT g.id::text FROM chat_groups g JOIN group_members m ON m.group_id=g.id WHERE g.id=$1 AND m.user_id=$2 AND g.status='active' FOR UPDATE OF g`, group, actor).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE chat_groups SET status='closed',closed_at=now(),closed_by=$2 WHERE id=$1`, group, actor); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE group_assistants SET enabled=false WHERE group_id=$1`, group); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE agent_runs SET status='cancelled',lease_expires_at=NULL,finished_at=now(),error_code='group_closed' WHERE group_id=$1 AND status IN ('queued','running')`, group); err != nil {
		return err
	}
	payload, _ := json.Marshal(notification.Event{Type: "group_closed", GroupID: group})
	if _, err = tx.Exec(ctx, `SELECT pg_notify($1,$2)`, notification.Channel, string(payload)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
