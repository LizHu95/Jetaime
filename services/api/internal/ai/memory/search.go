package memory

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/telemetry"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
)

type SearchInput struct {
	MaxSeq  int64      `json:"-"` // Trusted caller fence; zero only for standalone internal queries.
	Query   string     `json:"query"`
	Limit   int        `json:"limit"`
	Kind    string     `json:"kind,omitempty"`
	Subject string     `json:"subject,omitempty"`
	Since   *time.Time `json:"since,omitempty"`
	Until   *time.Time `json:"until,omitempty"`
}
type SearchResult struct {
	Documents         []Document `json:"documents"`
	RecentUnprocessed []Message  `json:"recentUnprocessed"`
	RecentOmitted     bool       `json:"recentOmitted"`
	Cursor            int64      `json:"cursorSeq"`
}

func (s *Store) Authorize(ctx context.Context, group, actor string) error {
	if _, err := uuid.Parse(group); err != nil {
		return ErrInvalid
	}
	var ok bool
	err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM chat_groups g JOIN group_members m ON m.group_id=g.id WHERE g.id=$1 AND m.user_id=$2 AND g.status='active')`, group, actor).Scan(&ok)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	return nil
}
func (s *Service) Search(ctx context.Context, group, actor string, in SearchInput) (SearchResult, error) {
	out := SearchResult{Documents: []Document{}, RecentUnprocessed: []Message{}}
	if err := s.Store.Authorize(ctx, group, actor); err != nil {
		return out, err
	}
	if len(strings.TrimSpace(in.Query)) == 0 || len(in.Query) > 3000 || in.MaxSeq < 0 || in.Limit < 0 || in.Limit > 20 || in.Kind != "" && in.Kind != "history" && !validKind(in.Kind) {
		return out, ErrInvalid
	}
	if in.Limit == 0 {
		in.Limit = 5
	}
	if in.Subject != "" && in.Subject != "shared" {
		if _, err := uuid.Parse(in.Subject); err != nil {
			return out, ErrInvalid
		}
	}
	if in.Since != nil && in.Until != nil && in.Since.After(*in.Until) {
		return out, ErrInvalid
	}
	if s.Embedder == nil {
		return out, ErrUnavailable
	}
	v, err := s.Embedder.Embed(ctx, in.Query)
	if err != nil {
		return out, err
	}
	if err = validateVector(v, s.Config.Embedding.Dimension); err != nil {
		return out, err
	}
	scores := map[string]float64{}
	docs := map[string]Document{}
	for _, route := range []string{"keyword", "vector"} {
		branchCtx, branch := telemetry.Start(ctx, "memory.search."+route, "RETRIEVER")
		found, e := s.Store.searchDocuments(branchCtx, group, actor, in, v, route)
		branch.SetAttributes(attribute.Int("retrieval.result_count", len(found)))
		var branchError error
		if e != nil {
			branchError = errors.New("retrieval query failed")
		}
		telemetry.End(branch, branchError)
		if e != nil {
			return out, e
		}
		for i, d := range found {
			docs[d.ID] = d
			scores[d.ID] += 1 / float64(60+i+1)
		}
	}
	for id, d := range docs {
		d.Score = scores[id]
		out.Documents = append(out.Documents, d)
	}
	sort.Slice(out.Documents, func(i, j int) bool {
		if out.Documents[i].Score == out.Documents[j].Score {
			return out.Documents[i].ID < out.Documents[j].ID
		}
		return out.Documents[i].Score > out.Documents[j].Score
	})
	if len(out.Documents) > in.Limit {
		out.Documents = out.Documents[:in.Limit]
	}
	// Recheck membership after provider I/O and enforce it in every evidence query.
	if err = s.Store.Authorize(ctx, group, actor); err != nil {
		return SearchResult{}, err
	}
	err = s.Store.Pool.QueryRow(ctx, `SELECT COALESCE((SELECT cursor_seq FROM memory_progress WHERE group_id=$1),0)`, group).Scan(&out.Cursor)
	if err != nil {
		return out, err
	}
	rows, err := s.Store.Pool.Query(ctx, `SELECT x.id::text,x.seq,x.sender_user_id::text,u.nickname,x.content,x.created_at FROM messages x
 JOIN users u ON u.id=x.sender_user_id JOIN chat_groups g ON g.id=x.group_id JOIN group_members m ON m.group_id=g.id AND m.user_id=$2
 WHERE x.group_id=$1 AND g.status='active' AND x.seq>$3 AND x.sender_type='user'
 AND ($4::timestamptz IS NULL OR x.created_at>=$4) AND ($5::timestamptz IS NULL OR x.created_at<=$5)
 AND ($6::bigint=0 OR x.seq<=$6) ORDER BY x.seq DESC LIMIT 31`, group, actor, out.Cursor, in.Since, in.Until, in.MaxSeq)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var m Message
		if err = rows.Scan(&m.ID, &m.Seq, &m.SenderID, &m.SenderName, &m.Text, &m.SentAt); err != nil {
			return out, err
		}
		out.RecentUnprocessed = append(out.RecentUnprocessed, m)
	}
	if len(out.RecentUnprocessed) > 30 {
		out.RecentOmitted = true
		out.RecentUnprocessed = out.RecentUnprocessed[:30]
	}
	// Output chronological recent context.
	for i, j := 0, len(out.RecentUnprocessed)-1; i < j; i, j = i+1, j-1 {
		out.RecentUnprocessed[i], out.RecentUnprocessed[j] = out.RecentUnprocessed[j], out.RecentUnprocessed[i]
	}
	return out, rows.Err()
}
func (s *Store) searchDocuments(ctx context.Context, group, actor string, in SearchInput, v []float32, route string) ([]Document, error) {
	args := []any{group, actor, in.Kind, in.Subject, in.Since, in.Until}
	query := `SELECT ` + documentColumns + ` FROM memory_documents d JOIN chat_groups g ON g.id=d.group_id
 JOIN group_members m ON m.group_id=g.id AND m.user_id=$2 `
	if route == "vector" {
		query += `JOIN memory_embeddings e ON e.group_id=d.group_id AND e.content_hash=d.content_hash AND e.status='ready' AND e.model_version=$7 AND e.dimension=$8 `
		args = append(args, s.Config.ModelVersion(), s.Config.Embedding.Dimension, vectorText(v))
	}
	query += `WHERE d.group_id=$1 AND g.status='active' AND d.active AND ($3='' OR d.kind=$3) AND ($4='' OR d.subject=$4)
 AND ($5::timestamptz IS NULL OR d.source_end_at>=$5) AND ($6::timestamptz IS NULL OR d.source_start_at<=$6) `

	order := `ORDER BY e.embedding OPERATOR(public.<=>) $9::public.vector,d.id LIMIT 20`
	if route == "keyword" {
		args = append(args, in.Query)
		query += `AND strpos(lower(d.content),lower($7))>0 `
		order = `ORDER BY d.source_end_at DESC,d.id LIMIT 20`
	}
	if in.MaxSeq > 0 {
		args = append(args, in.MaxSeq)
		query += fmt.Sprintf(`AND NOT EXISTS(SELECT 1 FROM memory_document_sources fence JOIN messages fm ON fm.group_id=fence.group_id AND fm.id=fence.message_id WHERE fence.document_id=d.id AND fm.seq>$%d) `, len(args))
	}
	query += order

	rows, err := s.Pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Document{}
	for rows.Next() {
		d, e := scanDocument(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
func (s *Store) Related(ctx context.Context, group string, c Candidate, v []float32) ([]Document, error) {
	// Identity filtering precedes similarity. Unembedded memories remain eligible,
	// so updates do not race the asynchronous embedding queue.
	rows, err := s.Pool.Query(ctx, `SELECT `+documentColumns+` FROM memory_documents d
 LEFT JOIN memory_embeddings e ON e.group_id=d.group_id AND e.content_hash=d.content_hash AND e.status='ready' AND e.model_version=$6 AND e.dimension=$7
 WHERE d.group_id=$1 AND d.active AND d.kind=$2 AND d.subject=$3 AND d.event_key=$4 AND d.temporal_scope=$5
 ORDER BY (d.content_hash=$9) DESC, e.embedding OPERATOR(public.<=>) $8::public.vector NULLS LAST,d.created_at DESC LIMIT 8`, group, c.Kind, c.Subject, c.EventKey, c.TemporalScope, s.Config.ModelVersion(), s.Config.Embedding.Dimension, vectorText(v), hash(c.Content))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Document{}
	for rows.Next() {
		d, e := scanDocument(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
