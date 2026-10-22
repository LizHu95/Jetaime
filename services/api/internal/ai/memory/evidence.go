package memory

import (
	"context"
	"fmt"
)

const evidenceColumns = `x.id::text,x.seq,COALESCE(x.sender_user_id::text,x.sender_assistant_id::text),COALESCE(u.nickname,a.name),x.content,x.created_at,x.sender_type`
const evidenceJoins = ` FROM messages x JOIN chat_groups g ON g.id=x.group_id JOIN group_members member ON member.group_id=g.id AND member.user_id=$2
 LEFT JOIN users u ON u.id=x.sender_user_id LEFT JOIN group_assistants a ON a.id=x.sender_assistant_id `

func (s *Store) RecentMessages(ctx context.Context, group, actor string, end int64, limit int) ([]Message, error) {
	if end <= 0 || limit < 1 || limit > 80 {
		return nil, ErrInvalid
	}
	if err := s.Authorize(ctx, group, actor); err != nil {
		return nil, err
	}
	rows, err := s.Pool.Query(ctx, `SELECT `+evidenceColumns+evidenceJoins+`WHERE x.group_id=$1 AND g.status='active' AND x.seq<=$3 ORDER BY x.seq DESC LIMIT $4`, group, actor, end, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		var m Message
		if err = rows.Scan(&m.ID, &m.Seq, &m.SenderID, &m.SenderName, &m.Text, &m.SentAt, &m.SenderType); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}
func (s *Store) SourceMessages(ctx context.Context, group, actor string, end int64, ids []string) ([]Message, error) {
	if end <= 0 || len(ids) > 100 {
		return nil, ErrInvalid
	}
	if err := s.Authorize(ctx, group, actor); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return []Message{}, nil
	}
	// Fetch bounded neighbours with original sequence ordering. Never load beyond
	// the original question, even when retrying after the conversation advanced.
	rows, err := s.Pool.Query(ctx, `SELECT `+evidenceColumns+evidenceJoins+`WHERE x.group_id=$1 AND g.status='active' AND x.seq<=$3 AND EXISTS
 (SELECT 1 FROM messages source WHERE source.group_id=$1 AND source.id=ANY($4::uuid[]) AND source.seq<=$3 AND x.seq BETWEEN source.seq-2 AND source.seq+2)
 ORDER BY x.seq LIMIT 80`, group, actor, end, unique(ids))
	if err != nil {
		return nil, fmt.Errorf("read QA evidence: %w", err)
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		var m Message
		if err = rows.Scan(&m.ID, &m.Seq, &m.SenderID, &m.SenderName, &m.Text, &m.SentAt, &m.SenderType); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
