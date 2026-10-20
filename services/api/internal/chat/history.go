package chat

import (
	"context"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai"
)

const MaxHistoryMessages = 1000
const MaxHistorySpan = 7 * 24 * time.Hour

// HistoryMessages reads only the authorized conversation at the original job's
// sequence fence. One extra row detects overflow without loading all history.
func (s *Service) HistoryMessages(ctx context.Context, group, actor string, fence int64, start, end time.Time) ([]ai.Message, error) {
	if !validID(group) || !validID(actor) || fence <= 0 || start.IsZero() || !end.After(start) || end.Sub(start) > MaxHistorySpan {
		return nil, ErrInvalid
	}
	if _, err := s.store.State(ctx, group, actor); err != nil {
		return nil, err
	}
	rows, err := s.store.pool.Query(ctx, `SELECT x.id::text,x.group_id::text,
 COALESCE(x.sender_user_id,x.sender_assistant_id)::text,x.created_at,x.content
 FROM messages x JOIN chat_groups g ON g.id=x.group_id
 JOIN group_members m ON m.group_id=g.id AND m.user_id=$2
 WHERE x.group_id=$1 AND g.status='active' AND x.seq<=$3
 AND x.created_at >= $4 AND x.created_at < $5
 AND x.sender_type IN ('user','assistant')
 ORDER BY x.created_at,x.seq LIMIT $6`, group, actor, fence, start, end, MaxHistoryMessages+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	messages := []ai.Message{}
	for rows.Next() {
		var message ai.Message
		if err := rows.Scan(&message.ID, &message.GroupID, &message.SenderID, &message.SentAt, &message.Text); err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	return messages, rows.Err()
}
