package memory

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type embeddingJob struct{ Group, Hash, Content, Token string }

func (s *Store) RepairQueue(ctx context.Context) error {
	_, err := s.Pool.Exec(ctx, `INSERT INTO memory_embeddings(group_id,model_version,dimension,content_hash,content,model_name,model_revision)
 SELECT DISTINCT d.group_id,$1::text,$2::integer,d.content_hash,d.content,$3::text,$4::text FROM memory_documents d
 JOIN chat_groups g ON g.id=d.group_id JOIN group_assistants a ON a.group_id=g.id
 WHERE d.active AND g.status='active' AND a.enabled ON CONFLICT DO NOTHING`, s.Config.ModelVersion(), s.Config.Embedding.Dimension, s.Config.Embedding.Model, s.Config.Embedding.Revision)
	return err
}
func (s *Store) claimEmbedding(ctx context.Context, group, key string) (embeddingJob, error) {
	var j embeddingJob
	j.Token = uuid.NewString()
	err := s.Pool.QueryRow(ctx, `WITH pick AS (
 SELECT e.group_id,e.content_hash FROM memory_embeddings e
 JOIN chat_groups g ON g.id=e.group_id JOIN group_assistants a ON a.group_id=g.id
 WHERE e.model_version=$1 AND e.dimension=$2 AND g.status='active' AND a.enabled
 AND e.status<>'ready' AND e.retry_at<=now() AND (e.lease_until IS NULL OR e.lease_until<now())
 AND ($3='' OR e.group_id::text=$3) AND ($4='' OR e.content_hash=$4)
 ORDER BY e.updated_at FOR UPDATE OF e SKIP LOCKED LIMIT 1)
 UPDATE memory_embeddings e SET status='running',lease_token=$5,lease_until=now()+interval '2 minutes',attempts=e.attempts+1
 FROM pick WHERE e.group_id=pick.group_id AND e.content_hash=pick.content_hash AND e.model_version=$1 AND e.dimension=$2
 RETURNING e.group_id::text,e.content_hash,e.content`, s.Config.ModelVersion(), s.Config.Embedding.Dimension, group, key, j.Token).Scan(&j.Group, &j.Hash, &j.Content)
	if errors.Is(err, pgx.ErrNoRows) {
		return j, ErrNotFound
	}
	return j, err
}
func (s *Store) finishEmbedding(ctx context.Context, j embeddingJob, v []float32) error {
	if err := validateVector(v, s.Config.Embedding.Dimension); err != nil {
		return err
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE memory_embeddings e SET status='ready',embedding=$6::public.vector,lease_until=NULL,lease_token=NULL,last_error=NULL,updated_at=now()
 FROM chat_groups g,group_assistants a WHERE e.group_id=$1 AND e.model_version=$2 AND e.dimension=$3 AND e.content_hash=$4
 AND e.lease_token=$5 AND e.lease_until>now() AND g.id=e.group_id AND g.status='active' AND a.group_id=g.id AND a.enabled`, j.Group, s.Config.ModelVersion(), s.Config.Embedding.Dimension, j.Hash, j.Token, vectorText(v))
	if err == nil && tag.RowsAffected() != 1 {
		return ErrLease
	}
	return err
}
func (s *Store) failEmbedding(ctx context.Context, j embeddingJob) error {
	_, err := s.Pool.Exec(ctx, `UPDATE memory_embeddings SET status='pending',lease_until=NULL,lease_token=NULL,last_error='embedding_failed',retry_at=now()+interval '30 seconds'*LEAST(attempts,20),updated_at=now()
 WHERE group_id=$1 AND model_version=$2 AND dimension=$3 AND content_hash=$4 AND lease_token=$5`, j.Group, s.Config.ModelVersion(), s.Config.Embedding.Dimension, j.Hash, j.Token)
	return err
}
func (s *Store) CachedEmbedding(ctx context.Context, embed Embedder, group, text string) ([]float32, error) {
	_, err := s.Pool.Exec(ctx, `INSERT INTO memory_embeddings(group_id,model_version,dimension,content_hash,content,model_name,model_revision)
 SELECT g.id,$2,$3,$4,$5,$6,$7 FROM chat_groups g JOIN group_assistants a ON a.group_id=g.id WHERE g.id=$1 AND g.status='active' AND a.enabled ON CONFLICT DO NOTHING`, group, s.Config.ModelVersion(), s.Config.Embedding.Dimension, hash(text), text, s.Config.Embedding.Model, s.Config.Embedding.Revision)
	if err != nil {
		return nil, err
	}
	var raw string
	err = s.Pool.QueryRow(ctx, `SELECT embedding::text FROM memory_embeddings WHERE group_id=$1 AND model_version=$2 AND dimension=$3 AND content_hash=$4 AND status='ready'`, group, s.Config.ModelVersion(), s.Config.Embedding.Dimension, hash(text)).Scan(&raw)
	if err == nil {
		var v []float32
		err = json.Unmarshal([]byte(raw), &v)
		return v, err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	j, err := s.claimEmbedding(ctx, group, hash(text))
	if errors.Is(err, ErrNotFound) {
		return nil, ErrBusy
	}
	if err != nil {
		return nil, err
	}
	return s.runEmbedding(ctx, embed, j)
}
func (s *Store) runEmbedding(ctx context.Context, embed Embedder, j embeddingJob) ([]float32, error) {
	v, err := embed.Embed(ctx, j.Content)
	if err == nil {
		err = s.finishEmbedding(ctx, j, v)
	}
	if err != nil {
		save, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = s.failEmbedding(save, j)
	}
	return v, err
}
