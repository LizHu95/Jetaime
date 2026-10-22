package chat

import (
	"context"

	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
	"github.com/LizHu95/Jetaime/services/api/internal/reminder"
	"github.com/jackc/pgx/v5"
)

// ReminderOperation keeps AI-job authorization out of the reminder domain.
// Its guard runs after the domain locks the group, fencing recovery and close.
func ReminderOperation(scope tools.Scope) (reminder.Operation, error) {
	request := scope.Context.Request
	if request == nil || request.JobID == "" {
		return reminder.Operation{}, reminder.ErrConflict
	}
	return reminder.Operation{ID: request.MessageID, SourceMessageID: request.MessageID, Guard: func(ctx context.Context, tx pgx.Tx) error {
		var authorized bool
		err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM messages m JOIN agent_runs r ON r.trigger_message_id=m.id WHERE m.id=$1 AND m.group_id=$2 AND m.sender_user_id=$3 AND m.seq=$4 AND r.status='running' AND r.lease_expires_at>now() AND r.id=$5 AND r.attempt_count=$6)`, request.MessageID, scope.Context.GroupID, scope.Context.ActorID, scope.ContextEndSeq, request.JobID, request.JobAttempt).Scan(&authorized)
		if err != nil {
			return err
		}
		if !authorized {
			return reminder.ErrConflict
		}
		return nil
	}}, nil
}
