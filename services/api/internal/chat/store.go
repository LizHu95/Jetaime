package chat

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/auth"
	"github.com/LizHu95/Jetaime/services/api/internal/collaboration"
	"github.com/LizHu95/Jetaime/services/api/internal/notification"
	"github.com/LizHu95/Jetaime/services/api/internal/telemetry"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/attribute"
)

const notificationChannel = notification.Channel

type Store struct{ pool *pgxpool.Pool }

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }
func rollback(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}
func notify(ctx context.Context, tx pgx.Tx, event Event) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `SELECT pg_notify($1,$2)`, notificationChannel, string(data))
	return err
}
func lockGroup(ctx context.Context, tx pgx.Tx, group, actor string) error {
	var id string
	err := tx.QueryRow(ctx, `SELECT g.id::text FROM chat_groups g JOIN group_members m ON m.group_id=g.id
 WHERE g.id=$1 AND m.user_id=$2 AND g.status='active' FOR UPDATE OF g`, group, actor).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
func (s *Store) State(ctx context.Context, group, actor string) (State, error) {
	var state State
	err := s.pool.QueryRow(ctx, `SELECT g.id::text,g.last_message_seq,m.last_read_seq,a.name,g.proactive_enabled,
 (SELECT count(*) FROM messages x WHERE x.group_id=g.id AND x.seq>m.last_read_seq AND x.sender_user_id IS DISTINCT FROM $2::uuid)
 FROM chat_groups g JOIN group_members m ON m.group_id=g.id AND m.user_id=$2
 JOIN group_assistants a ON a.group_id=g.id WHERE g.id=$1 AND g.status='active'`, group, actor).Scan(&state.ID, &state.LastSeq, &state.ReadSeq, &state.AssistantName, &state.ProactiveEnabled, &state.Unread)
	if errors.Is(err, pgx.ErrNoRows) {
		return state, ErrNotFound
	}
	if err != nil {
		return state, err
	}
	rows, err := s.pool.Query(ctx, `SELECT u.id::text,c.username,u.nickname FROM group_members m JOIN users u ON u.id=m.user_id JOIN user_credentials c ON c.user_id=u.id WHERE m.group_id=$1 ORDER BY u.id`, group)
	if err != nil {
		return state, err
	}
	defer rows.Close()
	state.Members = []auth.User{}
	for rows.Next() {
		var user auth.User
		if err := rows.Scan(&user.ID, &user.Username, &user.Nickname); err != nil {
			return state, err
		}
		state.Members = append(state.Members, user)
	}
	return state, rows.Err()
}

const messageSelect = `SELECT x.id::text,x.group_id::text,x.seq,x.sender_type,x.sender_user_id::text,
 COALESCE(u.nickname,a.name),x.content,x.mentions_ai,x.client_message_id::text,x.reply_to_id::text,x.created_at,x.agent_kind,x.agent_result
 FROM messages x LEFT JOIN users u ON u.id=x.sender_user_id LEFT JOIN group_assistants a ON a.id=x.sender_assistant_id `

func scanMessage(row pgx.Row) (Message, error) {
	var m Message
	err := row.Scan(&m.ID, &m.GroupID, &m.Seq, &m.SenderType, &m.SenderID, &m.SenderName, &m.Content, &m.MentionsAI, &m.ClientID, &m.ReplyToID, &m.CreatedAt, &m.AgentKind, &m.AgentResult)
	if errors.Is(err, pgx.ErrNoRows) {
		return m, ErrNotFound
	}
	return m, err
}
func (s *Store) Messages(ctx context.Context, group, actor string, after, before int64, limit int) (Page, error) {
	// Include membership in the data query as well as the access check, so closed
	// groups and other users' messages cannot leak through an unchecked selector.
	if _, err := s.State(ctx, group, actor); err != nil {
		return Page{}, err
	}
	order := "ASC"
	if before > 0 {
		order = "DESC"
	}
	rows, err := s.pool.Query(ctx, messageSelect+`JOIN chat_groups g ON g.id=x.group_id JOIN group_members m ON m.group_id=g.id
 WHERE x.group_id=$1 AND m.user_id=$2 AND g.status='active' AND x.seq>$3 AND ($4::bigint=0 OR x.seq<$4)
 ORDER BY x.seq `+order+` LIMIT $5`, group, actor, after, before, limit+1)
	if err != nil {
		return Page{}, err
	}
	defer rows.Close()
	page := Page{Messages: []Message{}}
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return Page{}, err
		}
		page.Messages = append(page.Messages, m)
	}
	if err := rows.Err(); err != nil {
		return Page{}, err
	}
	if len(page.Messages) > limit {
		page.HasMore = true
		page.Messages = page.Messages[:limit]
	}
	if before > 0 {
		for i, j := 0, len(page.Messages)-1; i < j; i, j = i+1, j-1 {
			page.Messages[i], page.Messages[j] = page.Messages[j], page.Messages[i]
		}
	}
	return page, nil
}
func (s *Store) Send(ctx context.Context, group, actor string, input SendInput, aiEnabled bool) (Message, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Message{}, err
	}
	defer rollback(tx)
	if err = lockGroup(ctx, tx, group, actor); err != nil {
		return Message{}, err
	}
	existing, err := scanMessage(tx.QueryRow(ctx, messageSelect+`WHERE x.sender_user_id=$1 AND x.client_message_id=$2`, actor, input.ClientID))
	if err == nil {
		if existing.GroupID != group || existing.Content != input.Content || existing.MentionsAI != input.MentionsAI || existing.AgentKind != input.AgentKind {
			return Message{}, ErrConflict
		}
		return existing, tx.Commit(ctx)
	}
	if !errors.Is(err, ErrNotFound) {
		return Message{}, err
	}
	var seq int64
	if err = tx.QueryRow(ctx, `UPDATE chat_groups SET last_message_seq=last_message_seq+1,last_message_at=now() WHERE id=$1 RETURNING last_message_seq`, group).Scan(&seq); err != nil {
		return Message{}, err
	}
	id := uuid.NewString()
	if _, err = tx.Exec(ctx, `INSERT INTO messages(id,group_id,seq,sender_type,sender_user_id,content,mentions_ai,client_message_id,agent_kind) VALUES($1,$2,$3,'user',$4,$5,$6,$7,$8)`, id, group, seq, actor, input.Content, input.MentionsAI, input.ClientID, input.AgentKind); err != nil {
		var pgerr *pgconn.PgError
		if errors.As(err, &pgerr) && pgerr.Code == "23505" {
			return Message{}, ErrConflict
		}
		return Message{}, err
	}
	// A live clarification belongs only to its requesting member. Preserve the
	// original mention flag so HTTP retries still match the saved user message.
	followup := false
	if aiEnabled && !input.MentionsAI {
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM reminders WHERE group_id=$1 AND user_id=$2 AND status='draft' AND updated_at>now()-interval '24 hours')`, group, actor).Scan(&followup); err != nil {
			return Message{}, err
		}
	}
	if input.MentionsAI || followup {
		runKind := input.AgentKind
		if followup {
			runKind = "auto"
		}
		carrier := telemetry.Carrier(ctx)
		status := "queued"
		var code *string
		var finished *time.Time
		var traceID *string
		if !aiEnabled {
			status = "failed"
			traceID = recordedTraceID(ctx)
			c := "ai_unavailable"
			code = &c
			t := time.Now()
			finished = &t
		}
		if _, err = tx.Exec(ctx, `INSERT INTO agent_runs(id,group_id,assistant_id,trigger_message_id,requested_by,context_end_seq,status,error_code,finished_at,agent_kind,trace_parent,trace_state,trigger_mentions_ai,trace_id)
 SELECT $1,$2,a.id,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13 FROM group_assistants a WHERE a.group_id=$2 AND a.enabled`, uuid.NewString(), group, id, actor, seq, status, code, finished, runKind, carrier["traceparent"], carrier["tracestate"], input.MentionsAI, traceID); err != nil {
			return Message{}, err
		}
	}
	// Human activity invalidates in-flight proactive work under the same group lock.
	if err = cancelProactive(ctx, tx, group, "superseded"); err != nil {
		return Message{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE chat_groups SET proactive_message_id=$2,
 proactive_due_at=CASE WHEN proactive_enabled AND $3 AND NOT $4 THEN clock_timestamp()+interval '5 seconds' ELSE NULL END WHERE id=$1`, group, id, aiEnabled, input.MentionsAI || followup); err != nil {
		return Message{}, err
	}
	message, err := scanMessage(tx.QueryRow(ctx, messageSelect+`WHERE x.id=$1`, id))
	if err != nil {
		return Message{}, err
	}
	if err = notify(ctx, tx, Event{Type: "message", GroupID: group}); err != nil {
		return Message{}, err
	}
	return message, tx.Commit(ctx)
}
func (s *Store) Read(ctx context.Context, group, actor string, seq int64) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer rollback(tx)
	if err = lockGroup(ctx, tx, group, actor); err != nil {
		return 0, err
	}
	var read int64
	err = tx.QueryRow(ctx, `UPDATE group_members m SET last_read_seq=greatest(m.last_read_seq,least($3,g.last_message_seq)) FROM chat_groups g WHERE m.group_id=g.id AND g.id=$1 AND m.user_id=$2 RETURNING m.last_read_seq`, group, actor, seq).Scan(&read)
	if err != nil {
		return 0, err
	}
	if err = notify(ctx, tx, Event{Type: "read", GroupID: group, ActorID: actor}); err != nil {
		return 0, err
	}
	return read, tx.Commit(ctx)
}

// Close preserves the existing chat API while collaboration owns group lifecycle.
func (s *Store) Close(ctx context.Context, group, actor string) error {
	err := collaboration.NewPostgresStore(s.pool).Close(ctx, group, actor)
	if errors.Is(err, collaboration.ErrNotFound) {
		return ErrNotFound
	}
	return err
}

const runSelect = `SELECT id::text,group_id::text,assistant_id::text,trigger_message_id::text,requested_by::text,context_end_seq,status,attempt_count,error_code,response_message_id::text,agent_kind,trace_parent,trace_state,queued_at,trace_id,trigger_mode,recognition,trigger_mentions_ai FROM agent_runs `

func scanRun(row pgx.Row) (Run, error) {
	var r Run
	err := row.Scan(&r.ID, &r.GroupID, &r.AssistantID, &r.TriggerID, &r.RequestedBy, &r.ContextEnd, &r.Status, &r.Attempt, &r.ErrorCode, &r.ResponseID, &r.Kind, &r.TraceParent, &r.TraceState, &r.QueuedAt, &r.TraceID, &r.TriggerMode, &r.Recognition, &r.TriggerMentionsAI)
	return r, err
}
func (s *Store) Runs(ctx context.Context, group, actor string) ([]Run, error) {
	if _, err := s.State(ctx, group, actor); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, runSelect+`WHERE group_id=$1 ORDER BY context_end_seq DESC LIMIT 100`, group)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Run{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}
func (s *Store) Retry(ctx context.Context, group, actor, id string) (retryErr error) {
	ctx, span := telemetry.Start(ctx, "chat.ai.retry", "CHAIN", attribute.String("session.id", group), attribute.String("user.id", actor), attribute.String("chat.run_id", id))
	defer func() {
		var traceErr error
		if retryErr != nil {
			traceErr = errors.New("AI retry failed")
		}
		telemetry.End(span, traceErr)
	}()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err = lockGroup(ctx, tx, group, actor); err != nil {
		return err
	}
	r, err := scanRun(tx.QueryRow(ctx, runSelect+`WHERE id=$1 AND group_id=$2 AND requested_by=$3 FOR UPDATE`, id, group, actor))
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if r.Status == "queued" || r.Status == "running" || r.Status == "succeeded" {
		return tx.Commit(ctx)
	}
	if r.Status != "failed" {
		return ErrConflict
	}
	carrier := telemetry.Carrier(ctx)
	if _, err = tx.Exec(ctx, `UPDATE agent_runs SET status='queued',error_code=NULL,trace_id=NULL,finished_at=NULL,queued_at=now(),trace_parent=$2,trace_state=$3 WHERE id=$1`, id, carrier["traceparent"], carrier["tracestate"]); err != nil {
		return err
	}
	if err = notify(ctx, tx, Event{Type: "ai", GroupID: group}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) Message(ctx context.Context, group, actor, id string) (Message, error) {
	if _, err := s.State(ctx, group, actor); err != nil {
		return Message{}, err
	}
	return scanMessage(s.pool.QueryRow(ctx, messageSelect+`JOIN chat_groups g ON g.id=x.group_id JOIN group_members m ON m.group_id=g.id
 WHERE x.group_id=$1 AND m.user_id=$2 AND x.id=$3 AND g.status='active'`, group, actor, id))
}
