package contacts

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/auth"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresStore struct{ pool *pgxpool.Pool }

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool} }
func (s *PostgresStore) FindUser(ctx context.Context, username string) (auth.User, error) {
	var u auth.User
	err := s.pool.QueryRow(ctx, `SELECT u.id::text,c.username,u.nickname FROM user_credentials c JOIN users u ON u.id=c.user_id WHERE c.username=$1`, username).Scan(&u.ID, &u.Username, &u.Nickname)
	if errors.Is(err, pgx.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

const requestSelect = `SELECT r.id::text,f.id::text,fc.username,f.nickname,t.id::text,tc.username,t.nickname,r.status,r.group_id::text,r.created_at
 FROM contact_requests r JOIN users f ON f.id=r.from_user_id JOIN user_credentials fc ON fc.user_id=f.id
 JOIN users t ON t.id=r.to_user_id JOIN user_credentials tc ON tc.user_id=t.id `

func scanRequest(row pgx.Row) (Request, error) {
	var r Request
	err := row.Scan(&r.ID, &r.From.ID, &r.From.Username, &r.From.Nickname, &r.To.ID, &r.To.Username, &r.To.Nickname, &r.Status, &r.GroupID, &r.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}
func rollback(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}
func lockPair(ctx context.Context, tx pgx.Tx, a, b string) error {
	ids := []string{a, b}
	sort.Strings(ids)
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, ids[0]+":"+ids[1])
	return err
}
func (s *PostgresStore) Send(ctx context.Context, actor, target string) (Request, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Request{}, err
	}
	defer rollback(tx)
	if err = lockPair(ctx, tx, actor, target); err != nil {
		return Request{}, err
	}
	var connected bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM chat_groups WHERE user_low_id=least($1::uuid,$2::uuid) AND user_high_id=greatest($1::uuid,$2::uuid) AND status='active')`, actor, target).Scan(&connected)
	if err != nil {
		return Request{}, err
	}
	if connected {
		return Request{}, ErrConnected
	}
	r, err := scanRequest(tx.QueryRow(ctx, requestSelect+`WHERE r.user_low_id=least($1::uuid,$2::uuid) AND r.user_high_id=greatest($1::uuid,$2::uuid) AND r.status='pending'`, actor, target))
	if errors.Is(err, ErrNotFound) {
		id := uuid.NewString()
		if _, err = tx.Exec(ctx, `INSERT INTO contact_requests(id,from_user_id,to_user_id) VALUES($1,$2,$3)`, id, actor, target); err != nil {
			return Request{}, err
		}
		r, err = scanRequest(tx.QueryRow(ctx, requestSelect+`WHERE r.id=$1`, id))
	}
	if err != nil {
		return Request{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Request{}, err
	}
	return r, nil
}
func (s *PostgresStore) Requests(ctx context.Context, actor string) ([]Request, error) {
	rows, err := s.pool.Query(ctx, requestSelect+`WHERE r.from_user_id=$1 OR r.to_user_id=$1 ORDER BY r.created_at DESC LIMIT 100`, actor)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Request{}
	for rows.Next() {
		r, err := scanRequest(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}
func (s *PostgresStore) Resolve(ctx context.Context, actor, id, action string) (Request, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Request{}, err
	}
	defer rollback(tx)
	// Read participants first, then take the pair lock before the row lock. All
	// mutations use this lock order, including reverse sends.
	r, err := scanRequest(tx.QueryRow(ctx, requestSelect+`WHERE r.id=$1 AND (r.from_user_id=$2 OR r.to_user_id=$2)`, id, actor))
	if err != nil {
		return Request{}, err
	}
	if (action == "cancel" && r.From.ID != actor) || (action != "cancel" && r.To.ID != actor) {
		return Request{}, ErrForbidden
	}
	if err = lockPair(ctx, tx, r.From.ID, r.To.ID); err != nil {
		return Request{}, err
	}
	r, err = scanRequest(tx.QueryRow(ctx, requestSelect+`WHERE r.id=$1 FOR UPDATE OF r`, id))
	if err != nil {
		return Request{}, err
	}
	status := map[string]string{"accept": "accepted", "reject": "rejected", "cancel": "cancelled"}[action]
	if r.Status == status {
		return r, tx.Commit(ctx)
	}
	if r.Status != "pending" {
		return Request{}, ErrConflict
	}
	var groupID *string
	if action == "accept" {
		g := uuid.NewString()
		groupID = &g
		// The schema's active-pair index and deferred completeness trigger provide
		// the final guard even against writers outside this module.
		var connected bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM chat_groups WHERE user_low_id=least($1::uuid,$2::uuid) AND user_high_id=greatest($1::uuid,$2::uuid) AND status='active')`, r.From.ID, r.To.ID).Scan(&connected); err != nil {
			return Request{}, err
		}
		if connected {
			return Request{}, ErrConnected
		}
		if _, err = tx.Exec(ctx, `INSERT INTO chat_groups(id,user_low_id,user_high_id) VALUES($1,least($2::uuid,$3::uuid),greatest($2::uuid,$3::uuid))`, g, r.From.ID, r.To.ID); err != nil {
			return Request{}, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO group_members(group_id,user_id) VALUES($1,$2),($1,$3)`, g, r.From.ID, r.To.ID); err != nil {
			return Request{}, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO group_assistants(id,group_id,agent_key) VALUES($1,$2,'shared-assistant')`, uuid.NewString(), g); err != nil {
			return Request{}, err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE contact_requests SET status=$2,group_id=$3,resolved_at=now() WHERE id=$1`, id, status, groupID); err != nil {
		return Request{}, err
	}
	r, err = scanRequest(tx.QueryRow(ctx, requestSelect+`WHERE r.id=$1`, id))
	if err != nil {
		return Request{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Request{}, err
	}
	return r, nil
}
func (s *PostgresStore) Groups(ctx context.Context, actor string) ([]Group, error) {
	rows, err := s.pool.Query(ctx, `SELECT g.id::text,u.id::text,c.username,u.nickname,g.status,a.id::text,a.name,g.created_at,
 (SELECT count(*) FROM messages x WHERE x.group_id=g.id AND x.seq>m.last_read_seq AND x.sender_user_id IS DISTINCT FROM $1::uuid),
 m.last_read_seq,g.last_message_seq,COALESCE((SELECT content FROM messages x WHERE x.group_id=g.id ORDER BY seq DESC LIMIT 1),'')
 FROM chat_groups g JOIN group_members m ON m.group_id=g.id AND m.user_id=$1
 JOIN users u ON u.id=CASE WHEN g.user_low_id=$1 THEN g.user_high_id ELSE g.user_low_id END
 JOIN user_credentials c ON c.user_id=u.id JOIN group_assistants a ON a.group_id=g.id
 WHERE g.status='active' ORDER BY COALESCE(g.last_message_at,g.created_at) DESC`, actor)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Group{}
	for rows.Next() {
		var g Group
		if err := rows.Scan(&g.ID, &g.Peer.ID, &g.Peer.Username, &g.Peer.Nickname, &g.Status, &g.AssistantID, &g.AssistantName, &g.CreatedAt, &g.UnreadCount, &g.LastReadSeq, &g.LastMessageSeq, &g.LastMessage); err != nil {
			return nil, err
		}
		result = append(result, g)
	}
	return result, rows.Err()
}
