package chat

import (
	"context"

	"github.com/jackc/pgx/v5"
)

func (s *Store) ConnectEvents(ctx context.Context) (*pgx.Conn, error) {
	return pgx.ConnectConfig(ctx, s.pool.Config().ConnConfig.Copy())
}
func (s *Store) EventUsers(ctx context.Context, event Event) (map[string]bool, error) {
	rows, err := s.pool.Query(ctx, `SELECT m.user_id::text FROM group_members m JOIN chat_groups g ON g.id=m.group_id WHERE m.group_id=$1 AND (g.status='active' OR $2='group_closed')`, event.GroupID, event.Type)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := map[string]bool{}
	for rows.Next() {
		var user string
		if err := rows.Scan(&user); err != nil {
			return nil, err
		}
		if event.Type != "read" || event.ActorID == user {
			users[user] = true
		}
	}
	return users, rows.Err()
}
