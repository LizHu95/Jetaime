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

// Search combines document retrieval with recent originals not yet processed by memory.
func (s *Service) Search(ctx context.Context, group, actor string, in SearchInput) (SearchResult, error) {
	out, err := s.SearchDocuments(ctx, group, actor, in)
	if err != nil {
		return out, err
	}
	pending, err := s.UnprocessedMessages(ctx, group, actor, in, nil)
	out.Cursor, out.RecentUnprocessed, out.RecentOmitted = pending.Cursor, pending.RecentUnprocessed, pending.RecentOmitted
	return out, err
}

// SearchDocuments 只检索并融合历史文档，不额外读取近期未处理消息。
func (s *Service) SearchDocuments(ctx context.Context, group, actor string, in SearchInput) (SearchResult, error) {
	out := SearchResult{Documents: []Document{}, RecentUnprocessed: []Message{}}
	// 先验证群仍有效且调用者属于该群，再校验检索词、过滤条件和返回数量。
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
	// 将检索词转成语义向量，并检查维度等约束，供后面的向量召回使用。
	v, err := s.Embedder.Embed(embeddingContext(ctx, "search_query"), in.Query)
	if err != nil {
		return out, err
	}
	if err = validateVector(v, s.Config.Embedding.Dimension); err != nil {
		return out, err
	}
	// 两路各取最多 20 个候选：关键词路按完整检索词包含匹配，向量路按余弦距离排序。
	// 两路都限制群、有效文档及来源截止序号，具体查询见 searchDocuments。
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
		// 使用 RRF 融合排名：每路贡献 1/(60+名次)，两路命中同一文档时累加。
		// 比较的是名次，无需将关键词结果和向量距离换算到同一分数尺度。
		for i, d := range found {
			docs[d.ID] = d
			scores[d.ID] += 1 / float64(60+i+1)
		}
	}
	// 按文档 ID 合并候选，按融合分数降序返回；同分时用 ID 保持稳定顺序。
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
	if err := s.Store.Authorize(ctx, group, actor); err != nil {
		return SearchResult{}, err
	}
	return out, nil
}

// UnprocessedMessages 补读记忆游标之后的用户原文，排除调用方已经持有的消息。
// 排除条件在 LIMIT 之前生效，避免重复消息占用补充额度。
func (s *Service) UnprocessedMessages(ctx context.Context, group, actor string, in SearchInput, exclude []string) (SearchResult, error) {
	out := SearchResult{RecentUnprocessed: []Message{}}
	if in.MaxSeq < 0 || len(exclude) > 200 || (in.Since != nil && in.Until != nil && in.Since.After(*in.Until)) {
		return out, ErrInvalid
	}
	for _, id := range exclude {
		if _, err := uuid.Parse(id); err != nil {
			return out, ErrInvalid
		}
	}
	// 外部向量请求和检索期间权限可能变化，因此再次鉴权；后续原文查询也检查成员关系。
	if err := s.Store.Authorize(ctx, group, actor); err != nil {
		return SearchResult{}, err
	}
	// 读取记忆处理游标；游标之后的用户消息可能尚未进入记忆文档，需要单独补充。
	err := s.Store.Pool.QueryRow(ctx, `SELECT COALESCE((SELECT cursor_seq FROM memory_progress WHERE group_id=$1),0)`, group).Scan(&out.Cursor)
	if err != nil {
		return out, err
	}
	// 按时间过滤与 MaxSeq 截止范围读取最新 31 条未处理用户消息。
	// 多取 1 条用于判断是否有省略，最终最多返回 30 条；这里不做相关性排序。
	rows, err := s.Store.Pool.Query(ctx, `SELECT x.id::text,x.seq,x.sender_user_id::text,u.nickname,x.content,x.created_at FROM messages x
 JOIN users u ON u.id=x.sender_user_id JOIN chat_groups g ON g.id=x.group_id JOIN group_members m ON m.group_id=g.id AND m.user_id=$2
 WHERE x.group_id=$1 AND g.status='active' AND x.seq>$3 AND x.sender_type='user'
 AND ($4::timestamptz IS NULL OR x.created_at>=$4) AND ($5::timestamptz IS NULL OR x.created_at<=$5)
 AND ($6::bigint=0 OR x.seq<=$6) AND NOT (x.id::text=ANY(COALESCE($7::text[],ARRAY[]::text[]))) ORDER BY x.seq DESC LIMIT 31`, group, actor, out.Cursor, in.Since, in.Until, in.MaxSeq, exclude)
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
	// 查询时倒序取最新消息，返回前反转为群内序号正序，便于模型阅读对话。
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
