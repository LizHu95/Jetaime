package reminder

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/notification"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct{ pool *pgxpool.Pool }

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool} }

// Operation identifies one caller request. Guard is an optional authorization
// hook run inside the transaction; AI job fencing is supplied by the chat layer.
type Operation struct {
	ID              string
	SourceMessageID string
	Guard           func(context.Context, pgx.Tx) error
}
type transaction struct {
	tx    pgx.Tx
	scope Scope
}

func rollback(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}
func lockGroup(ctx context.Context, tx pgx.Tx, scope Scope) error {
	if !validID(scope.GroupID) || !validID(scope.UserID) {
		return ErrInvalid
	}
	var id string
	err := tx.QueryRow(ctx, `SELECT g.id::text FROM chat_groups g JOIN group_members m ON m.group_id=g.id WHERE g.id=$1 AND m.user_id=$2 AND g.status='active' FOR UPDATE OF g`, scope.GroupID, scope.UserID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
func notify(ctx context.Context, tx pgx.Tx, group string) error {
	data, _ := json.Marshal(map[string]string{"type": "message", "groupId": group})
	_, err := tx.Exec(ctx, `SELECT pg_notify($1,$2)`, notification.Channel, string(data))
	return err
}
func (s *Store) mutate(ctx context.Context, scope Scope, op Operation, fn func(*transaction) (Result, error)) (Result, error) {
	if !validID(op.ID) || (op.SourceMessageID != "" && !validID(op.SourceMessageID)) {
		return Result{}, ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Result{}, err
	}
	defer rollback(tx)
	if err = lockGroup(ctx, tx, scope); err != nil {
		return Result{}, err
	}
	if op.Guard != nil {
		if err = op.Guard(ctx, tx); err != nil {
			return Result{}, err
		}
	}
	var cached []byte
	var group, user string
	err = tx.QueryRow(ctx, `SELECT group_id::text,user_id::text,result FROM reminder_operations WHERE operation_id=$1`, op.ID).Scan(&group, &user, &cached)
	if err == nil {
		if group != scope.GroupID || user != scope.UserID {
			return Result{}, ErrConflict
		}
		var result Result
		if err = json.Unmarshal(cached, &result); err != nil {
			return Result{}, err
		}
		return result, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Result{}, err
	}
	if op.SourceMessageID != "" {
		var exists bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM messages WHERE id=$1 AND group_id=$2 AND sender_user_id=$3)`, op.SourceMessageID, scope.GroupID, scope.UserID).Scan(&exists); err != nil {
			return Result{}, err
		}
		if !exists {
			return Result{}, ErrNotFound
		}
	}
	result, err := fn(&transaction{tx, scope})
	if err != nil {
		return Result{}, err
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return Result{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO reminder_operations(operation_id,group_id,user_id,source_message_id,result) VALUES($1,$2,$3,$4,$5)`, op.ID, scope.GroupID, scope.UserID, nullString(op.SourceMessageID), raw)
	if err != nil {
		return Result{}, err
	}
	if err = notify(ctx, tx, scope.GroupID); err != nil {
		return Result{}, err
	}
	return result, tx.Commit(ctx)
}
func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

const reminderSelect = `SELECT r.id::text,r.user_id::text,u.nickname,r.title,r.local_date,r.local_time,r.due_at,r.timezone,r.status,r.version,r.updated_at,r.sent_at FROM reminders r JOIN users u ON u.id=r.user_id `

func scanReminder(row pgx.Row) (Reminder, error) {
	var r Reminder
	err := row.Scan(&r.ID, &r.UserID, &r.UserName, &r.Title, &r.Date, &r.Time, &r.DueAt, &r.Timezone, &r.Status, &r.Version, &r.UpdatedAt, &r.SentAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}
func (tx *transaction) get(ctx context.Context, id string) (Reminder, error) {
	if !validID(id) {
		return Reminder{}, ErrInvalid
	}
	return scanReminder(tx.tx.QueryRow(ctx, reminderSelect+`WHERE r.id=$1 AND r.group_id=$2 FOR UPDATE OF r`, id, tx.scope.GroupID))
}
func (tx *transaction) draft(ctx context.Context) (*Reminder, error) {
	r, err := scanReminder(tx.tx.QueryRow(ctx, reminderSelect+`WHERE r.group_id=$1 AND r.user_id=$2 AND r.status='draft' AND r.updated_at>now()-interval '24 hours'`, tx.scope.GroupID, tx.scope.UserID))
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	return &r, err
}
func (tx *transaction) expireDraft(ctx context.Context) error {
	_, err := tx.tx.Exec(ctx, `UPDATE reminders SET status='cancelled',updated_at=now(),version=version+1 WHERE group_id=$1 AND user_id=$2 AND status='draft' AND updated_at<=now()-interval '24 hours'`, tx.scope.GroupID, tx.scope.UserID)
	return err
}
func (tx *transaction) insert(ctx context.Context, r Reminder, source string) error {
	_, err := tx.tx.Exec(ctx, `INSERT INTO reminders(id,group_id,user_id,title,local_date,local_time,due_at,timezone,status,source_message_id,updated_at,version) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, r.ID, tx.scope.GroupID, tx.scope.UserID, r.Title, r.Date, r.Time, r.DueAt, r.Timezone, r.Status, nullString(source), r.UpdatedAt, r.Version)
	return err
}
func (tx *transaction) save(ctx context.Context, r Reminder) error {
	_, err := tx.tx.Exec(ctx, `UPDATE reminders SET title=$3,local_date=$4,local_time=$5,due_at=$6,status=$7,updated_at=$8,version=$9 WHERE id=$1 AND group_id=$2`, r.ID, tx.scope.GroupID, r.Title, r.Date, r.Time, r.DueAt, r.Status, r.UpdatedAt, r.Version)
	return err
}
func (s *Store) Draft(ctx context.Context, scope Scope) (*Reminder, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	if err = lockGroup(ctx, tx, scope); err != nil {
		return nil, err
	}
	r, err := (&transaction{tx, scope}).draft(ctx)
	if err != nil {
		return nil, err
	}
	return r, tx.Commit(ctx)
}
func (s *Store) List(ctx context.Context, scope Scope) ([]Reminder, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	if err = lockGroup(ctx, tx, scope); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, reminderSelect+`WHERE r.group_id=$1 AND r.status<>'draft' ORDER BY CASE WHEN r.status='pending' THEN 0 ELSE 1 END,r.due_at,r.id`, scope.GroupID)
	if err != nil {
		return nil, err
	}
	result := []Reminder{}
	for rows.Next() {
		r, err := scanReminder(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		result = append(result, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	return result, tx.Commit(ctx)
}

func (s *Store) Deliver(ctx context.Context) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer rollback(tx)
	var group string
	err = tx.QueryRow(ctx, `SELECT g.id::text FROM chat_groups g WHERE g.status='active' AND EXISTS(SELECT 1 FROM reminders r WHERE r.group_id=g.id AND r.status='pending' AND r.due_at<=now()) ORDER BY g.id FOR UPDATE OF g SKIP LOCKED LIMIT 1`).Scan(&group)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var id, title, name, assistant string
	err = tx.QueryRow(ctx, `SELECT r.id::text,r.title,u.nickname,a.id::text FROM reminders r JOIN users u ON u.id=r.user_id JOIN group_assistants a ON a.group_id=r.group_id WHERE r.group_id=$1 AND r.status='pending' AND r.due_at<=now() ORDER BY r.due_at,r.id LIMIT 1 FOR UPDATE OF r`, group).Scan(&id, &title, &name, &assistant)
	if err != nil {
		return false, err
	}
	var seq int64
	if err = tx.QueryRow(ctx, `UPDATE chat_groups SET last_message_seq=last_message_seq+1,last_message_at=now() WHERE id=$1 RETURNING last_message_seq`, group).Scan(&seq); err != nil {
		return false, err
	}
	message := uuid.NewString()
	if _, err = tx.Exec(ctx, `INSERT INTO messages(id,group_id,seq,sender_type,sender_assistant_id,content,agent_kind) VALUES($1,$2,$3,'assistant',$4,$5,'main')`, message, group, seq, assistant, fmt.Sprintf("⏰ %s，提醒时间到了：%s", name, title)); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `UPDATE reminders SET status='sent',delivery_message_id=$2,sent_at=now(),updated_at=now(),version=version+1 WHERE id=$1`, id, message); err != nil {
		return false, err
	}
	if err = notify(ctx, tx, group); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}
