package chat

import (
	"context"

	"github.com/LizHu95/Jetaime/services/api/internal/ai/tools"
)

// AuthorizeExternalTool checks live membership, assistant status and the original
// job lease before any external query. It does not grant device location access.
func (s *Service) AuthorizeExternalTool(ctx context.Context, scope tools.Scope) error {
	r := scope.Context.Request
	if r == nil || r.JobID == "" || r.SenderID != scope.Context.ActorID || scope.ContextEndSeq <= 0 {
		return ErrNotFound
	}
	var allowed bool
	err := s.store.pool.QueryRow(ctx, `SELECT EXISTS (
 SELECT 1 FROM chat_groups g
 JOIN group_members gm ON gm.group_id=g.id AND gm.user_id=$2
 JOIN group_assistants a ON a.group_id=g.id AND a.enabled=true
 JOIN messages m ON m.group_id=g.id AND m.id=$3 AND m.sender_user_id=$2 AND m.seq=$4
 JOIN agent_runs r ON r.group_id=g.id AND r.trigger_message_id=m.id
 WHERE g.id=$1 AND g.status='active'
 AND r.id=$5 AND r.status='running' AND r.lease_expires_at>now() AND r.attempt_count=$6
)`, scope.Context.GroupID, scope.Context.ActorID, r.MessageID, scope.ContextEndSeq, r.JobID, r.JobAttempt).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrNotFound
	}
	return nil
}
