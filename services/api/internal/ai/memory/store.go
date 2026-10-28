package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	Pool   *pgxpool.Pool
	Config Config
}

func NewStore(p *pgxpool.Pool, c Config) *Store { return &Store{p, c} }
func rollback(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}

// CheckPipeline validates compatibility without creating or resetting progress.
func (s *Store) CheckPipeline(ctx context.Context) error {
	var mismatch bool
	if err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM memory_progress WHERE pipeline<>$1)`, s.Config.Pipeline()).Scan(&mismatch); err != nil {
		return err
	}
	if mismatch {
		return ErrPipeline
	}
	return nil
}

func (s *Store) Initialize(ctx context.Context) error {
	if err := s.CheckPipeline(ctx); err != nil {
		return err
	}
	_, err := s.Pool.Exec(ctx, `INSERT INTO memory_progress(group_id,pipeline,backfill_end_seq)
 SELECT g.id,$1,g.last_message_seq FROM chat_groups g JOIN group_assistants a ON a.group_id=g.id
 WHERE g.status='active' AND a.enabled ON CONFLICT DO NOTHING`, s.Config.Pipeline())
	return err
}

// Claim fixes the upper message fence and persists it before any model request.
// The lease token fences stale workers after retries/restarts.
func (s *Store) Claim(ctx context.Context, force bool) (Batch, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Batch{}, err
	}
	defer rollback(tx)
	var b Batch
	err = tx.QueryRow(ctx, `SELECT p.group_id::text,p.cursor_seq FROM memory_progress p
 JOIN chat_groups g ON g.id=p.group_id JOIN group_assistants a ON a.group_id=g.id
 WHERE g.status='active' AND a.enabled AND p.pipeline=$1 AND p.retry_at<=now()
 AND (p.lease_until IS NULL OR p.lease_until<now())
 AND EXISTS(SELECT 1 FROM messages x WHERE x.group_id=p.group_id AND x.seq>p.cursor_seq)
 AND ($2 OR p.cursor_seq<p.backfill_end_seq
 OR (SELECT count(*) FROM messages x WHERE x.group_id=p.group_id AND x.seq>p.cursor_seq AND x.sender_type='user')>=15
 OR g.last_message_at<=now()-interval '5 minutes'
 OR EXISTS(SELECT 1 FROM messages x WHERE x.group_id=p.group_id AND x.seq>p.cursor_seq AND x.created_at<=now()-interval '30 minutes'))
 ORDER BY p.updated_at,p.group_id FOR UPDATE OF p SKIP LOCKED LIMIT 1`, s.Config.Pipeline(), force).Scan(&b.GroupID, &b.From)
	if errors.Is(err, pgx.ErrNoRows) {
		return b, ErrNotFound
	}
	if err != nil {
		return b, err
	}
	var raw []byte
	var decisions []byte
	err = tx.QueryRow(ctx, `SELECT to_seq,candidates,mutations FROM memory_batches WHERE group_id=$1 AND from_seq=$2 AND completed_at IS NULL`, b.GroupID, b.From).Scan(&b.To, &raw, &decisions)
	if err == nil {
		if err = json.Unmarshal(raw, &b.Candidates); err != nil {
			return b, err
		}
		b.Extracted = true
		if err = json.Unmarshal(decisions, &b.Mutations); err != nil {
			return b, err
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return b, err
	}
	// Include three prior user messages as extraction-only overlap. Sources for a
	// new candidate must include at least one message beyond the durable cursor.
	rows, err := tx.Query(ctx, `SELECT id::text,seq,COALESCE(sender_user_id::text,''),content,created_at,
 COALESCE((SELECT nickname FROM users WHERE id=sender_user_id),'AI')
 FROM messages WHERE group_id=$1 AND seq>COALESCE((SELECT min(seq)-1 FROM
 (SELECT seq FROM messages WHERE group_id=$1 AND seq<=$2 AND sender_type='user' ORDER BY seq DESC LIMIT 3) q),$2)
 AND ($3::bigint=0 OR seq<=$3) ORDER BY seq LIMIT 43`, b.GroupID, b.From, b.To)
	if err != nil {
		return b, err
	}
	size := 0
	for rows.Next() {
		var m Message
		if err = rows.Scan(&m.ID, &m.Seq, &m.SenderID, &m.Text, &m.SentAt, &m.SenderName); err != nil {
			rows.Close()
			return b, err
		}
		// Assistant messages advance the cursor but are not durable human facts.
		if m.SenderID == "" {
			if !b.Extracted {
				b.To = m.Seq
			}
			continue
		}
		cost := len(m.Text) + len(m.SenderName) + 256
		m.SentAt = m.SentAt.In(time.FixedZone("Asia/Shanghai", 8*3600))
		if m.Seq > b.From && size+cost > MaxBatchBytes {
			hasFresh := false
			for _, previous := range b.Messages {
				hasFresh = hasFresh || previous.Seq > b.From
			}
			if !hasFresh {
				b.Messages = nil
				size = 0
			} // Drop optional overlap before blocking a valid long message.
		}
		if size+cost > MaxBatchBytes && m.Seq <= b.From {
			continue
		}
		if size+cost > MaxBatchBytes && b.To > b.From && !b.Extracted {
			break
		}
		if size+cost > MaxBatchBytes {
			rows.Close()
			return b, fmt.Errorf("message exceeds memory batch capacity")
		}
		b.Messages = append(b.Messages, m)
		size += cost
		if !b.Extracted {
			b.To = m.Seq
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return b, err
	}
	if b.To <= b.From {
		return b, ErrInvalid
	}
	members, err := tx.Query(ctx, `SELECT u.id::text,u.nickname FROM group_members m JOIN users u ON u.id=m.user_id WHERE m.group_id=$1 ORDER BY u.id`, b.GroupID)
	if err != nil {
		return b, err
	}
	for members.Next() {
		var p Person
		if err = members.Scan(&p.ID, &p.Nickname); err != nil {
			members.Close()
			return b, err
		}
		b.Members = append(b.Members, p)
	}
	err = members.Err()
	members.Close()
	if err != nil {
		return b, err
	}
	b.Token = uuid.NewString()
	_, err = tx.Exec(ctx, `UPDATE memory_progress SET lease_token=$2,lease_until=now()+interval '5 minutes',attempts=attempts+1,updated_at=now() WHERE group_id=$1`, b.GroupID, b.Token)
	if err != nil {
		return b, err
	}
	return b, tx.Commit(ctx)
}
func (s *Store) checkLease(ctx context.Context, tx pgx.Tx, b Batch) error {
	var id string
	// Match the message/closure lock order: group first, progress second.
	if err := tx.QueryRow(ctx, `SELECT id::text FROM chat_groups WHERE id=$1 AND status='active' FOR UPDATE`, b.GroupID).Scan(&id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrLease
		}
		return err
	}
	err := tx.QueryRow(ctx, `SELECT p.group_id::text FROM memory_progress p JOIN chat_groups g ON g.id=p.group_id
 JOIN group_assistants a ON a.group_id=g.id
 WHERE p.group_id=$1 AND p.cursor_seq=$2 AND p.lease_token=$3 AND p.lease_until>now() AND g.status='active' AND a.enabled FOR UPDATE OF p,g`, b.GroupID, b.From, b.Token).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrLease
	}
	return err
}
func (s *Store) SaveExtraction(ctx context.Context, b Batch, c []Candidate) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err = s.checkLease(ctx, tx, b); err != nil {
		return err
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO memory_batches(group_id,from_seq,to_seq,candidates) VALUES ($1,$2,$3,$4)
 ON CONFLICT (group_id,from_seq) DO NOTHING`, b.GroupID, b.From, b.To, raw)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Store) Fail(ctx context.Context, b Batch) error {
	_, err := s.Pool.Exec(ctx, `UPDATE memory_progress SET lease_token=NULL,lease_until=NULL,
 retry_at=now()+interval '30 seconds'*LEAST(attempts,20),last_error='processing_failed',updated_at=now()
 WHERE group_id=$1 AND lease_token=$2`, b.GroupID, b.Token)
	return err
}

func queueEmbedding(ctx context.Context, tx pgx.Tx, c Config, group, text string) error {
	_, err := tx.Exec(ctx, `INSERT INTO memory_embeddings(group_id,model_version,dimension,content_hash,content,model_name,model_revision)
 VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING`, group, c.ModelVersion(), c.Embedding.Dimension, hash(text), text, c.Embedding.Model, c.Embedding.Revision)
	return err
}
func (s *Store) Complete(ctx context.Context, b Batch, mutations []Mutation) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err = s.checkLease(ctx, tx, b); err != nil {
		return err
	}
	for _, chunk := range chunks(b.Messages, b.From) {
		d := Document{ID: uuid.NewString(), LogicalID: uuid.NewString(), Revision: 1, Kind: "history", Subject: "shared", TemporalScope: "event", Status: "unknown", ChangeType: "none", Content: chunk.Content, SourceIDs: chunk.SourceIDs, Start: chunk.Start, End: chunk.End}
		if err = s.insertDocument(ctx, tx, b.GroupID, d, "ADD", true); err != nil {
			return err
		}
	}
	for _, m := range mutations {
		c, decision := m.Candidate, m.Decision
		if err = validateMetadata(c); err != nil {
			return fmt.Errorf("%w: %s", ErrInvalid, err)
		}
		var old Document
		if decision.Action != "ADD" {
			old, err = loadDocument(ctx, tx, b.GroupID, decision.TargetID)
			if err != nil {
				return err
			}
			if old.Kind != c.Kind || old.Subject != c.Subject || old.EventKey != c.EventKey || old.TemporalScope != c.TemporalScope {
				return ErrInvalid
			}
		}
		if err = validateDecision(c, decision, []Document{old}); err != nil {
			return err
		}
		if decision.Action == "NOOP" {
			if err = addEvidence(ctx, tx, b.GroupID, old.ID, c.SourceIDs); err != nil {
				return err
			}
			continue
		}
		text := strings.TrimSpace(decision.Content)
		if text == "" {
			text = c.Content
		}
		if decision.Action == "ADD" {
			// Exact identity+content duplicates become evidence-only updates.
			var existing string
			err = tx.QueryRow(ctx, `SELECT id::text FROM memory_documents WHERE group_id=$1 AND active AND kind=$2 AND subject=$3 AND event_key=$4 AND temporal_scope=$5 AND content_hash=$6 AND status=$7 AND valid_from IS NOT DISTINCT FROM $8::date AND valid_until IS NOT DISTINCT FROM $9::date`, b.GroupID, c.Kind, c.Subject, c.EventKey, c.TemporalScope, hash(text), c.Status, c.ValidFrom, c.ValidUntil).Scan(&existing)
			if err == nil {
				if err = addEvidence(ctx, tx, b.GroupID, existing, c.SourceIDs); err != nil {
					return err
				}
				continue
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		} else {
			if c.ChangeType == "none" {
				return ErrInvalid
			}
			if _, err = tx.Exec(ctx, `UPDATE memory_documents SET active=false WHERE group_id=$1 AND id=$2`, b.GroupID, old.ID); err != nil {
				return err
			}
		}
		d := Document{ID: uuid.NewString(), LogicalID: uuid.NewString(), Revision: 1, Kind: c.Kind, Subject: c.Subject, EventKey: c.EventKey, TemporalScope: c.TemporalScope, Status: c.Status, ValidFrom: c.ValidFrom, ValidUntil: c.ValidUntil, ChangeType: c.ChangeType, Content: text, SourceIDs: append([]string{}, c.SourceIDs...)}
		if decision.Action != "ADD" {
			d.LogicalID = old.LogicalID
			d.Revision = old.Revision + 1
			d.SourceIDs = unique(append(d.SourceIDs, old.SourceIDs...))
		}
		if err = tx.QueryRow(ctx, `SELECT min(created_at),max(created_at) FROM messages WHERE group_id=$1 AND id=ANY($2::uuid[])`, b.GroupID, d.SourceIDs).Scan(&d.Start, &d.End); err != nil {
			return err
		}
		if err = s.insertDocument(ctx, tx, b.GroupID, d, decision.Action, decision.Action != "INVALIDATE"); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE memory_batches SET completed_at=now() WHERE group_id=$1 AND from_seq=$2`, b.GroupID, b.From); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE memory_progress SET cursor_seq=$2,lease_token=NULL,lease_until=NULL,retry_at=now(),attempts=0,last_error=NULL,updated_at=now() WHERE group_id=$1`, b.GroupID, b.To)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Store) insertDocument(ctx context.Context, tx pgx.Tx, group string, d Document, action string, active bool) error {
	_, err := tx.Exec(ctx, `INSERT INTO memory_documents(id,group_id,logical_id,revision,kind,subject,event_key,temporal_scope,content,content_hash,active,change_action,source_start_at,source_end_at,status,valid_from,valid_until,change_type)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16::date,$17::date,$18)`, d.ID, group, d.LogicalID, d.Revision, d.Kind, d.Subject, d.EventKey, d.TemporalScope, d.Content, hash(d.Content), active, action, d.Start, d.End, d.Status, d.ValidFrom, d.ValidUntil, d.ChangeType)
	if err != nil {
		return err
	}
	for _, id := range unique(d.SourceIDs) {
		if _, err = tx.Exec(ctx, `INSERT INTO memory_document_sources VALUES($1,$2,$3)`, group, d.ID, id); err != nil {
			return err
		}
	}
	if active {
		return queueEmbedding(ctx, tx, s.Config, group, d.Content)
	}
	return nil
}
func unique(ids []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, id := range ids {
		if !seen[id] {
			out = append(out, id)
			seen[id] = true
		}
	}
	return out
}

type rowQuery interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

const documentColumns = `d.id::text,d.logical_id::text,d.revision,d.kind,d.subject,d.event_key,d.temporal_scope,d.content,d.status,to_char(d.valid_from,'YYYY-MM-DD'),to_char(d.valid_until,'YYYY-MM-DD'),d.change_type,d.source_start_at,d.source_end_at,
 ARRAY(SELECT s.message_id::text FROM memory_document_sources s JOIN messages m ON m.group_id=s.group_id AND m.id=s.message_id WHERE s.document_id=d.id ORDER BY m.seq)`

func scanDocument(row pgx.Row) (Document, error) {
	var d Document
	err := row.Scan(&d.ID, &d.LogicalID, &d.Revision, &d.Kind, &d.Subject, &d.EventKey, &d.TemporalScope, &d.Content, &d.Status, &d.ValidFrom, &d.ValidUntil, &d.ChangeType, &d.Start, &d.End, &d.SourceIDs)
	return d, err
}
func loadDocument(ctx context.Context, q rowQuery, group, id string) (Document, error) {
	return scanDocument(q.QueryRow(ctx, `SELECT `+documentColumns+` FROM memory_documents d WHERE group_id=$1 AND id=$2 AND active AND kind<>'history'`, group, id))
}

func addEvidence(ctx context.Context, tx pgx.Tx, group, id string, sources []string) error {
	for _, source := range unique(sources) {
		if _, err := tx.Exec(ctx, `INSERT INTO memory_document_sources VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, group, id, source); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, `UPDATE memory_documents d SET (source_start_at,source_end_at)=(SELECT min(m.created_at),max(m.created_at) FROM memory_document_sources s JOIN messages m ON m.group_id=s.group_id AND m.id=s.message_id WHERE s.document_id=d.id) WHERE d.group_id=$1 AND d.id=$2`, group, id)
	return err
}

// Checkpoint each completed reconciliation before starting another external call.
func (s *Store) SaveMutations(ctx context.Context, b Batch, mutations []Mutation) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err = s.checkLease(ctx, tx, b); err != nil {
		return err
	}
	raw, err := json.Marshal(mutations)
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE memory_batches SET mutations=$3 WHERE group_id=$1 AND from_seq=$2 AND completed_at IS NULL`, b.GroupID, b.From, raw)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrLease
	}
	return tx.Commit(ctx)
}
