package memory

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/LizHu95/Jetaime/services/api/internal/ai/memory/memorydb"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type embeddingJob struct{ Group, Hash, Content, Token string }

func (s *Store) RepairQueue(ctx context.Context) error {
	return memorydb.New(s.Pool).RepairEmbeddingQueue(ctx, memorydb.RepairEmbeddingQueueParams{
		ModelVersion:  s.Config.ModelVersion(),
		Dimension:     int32(s.Config.Embedding.Dimension),
		ModelName:     s.Config.Embedding.Model,
		ModelRevision: s.Config.Embedding.Revision,
	})
}
func (s *Store) claimEmbedding(ctx context.Context, group, key string) (embeddingJob, error) {
	var j embeddingJob
	j.Token = uuid.NewString()
	row, err := memorydb.New(s.Pool).ClaimEmbedding(ctx, memorydb.ClaimEmbeddingParams{
		ModelVersion: s.Config.ModelVersion(),
		Dimension:    int32(s.Config.Embedding.Dimension),
		GroupFilter:  group,
		HashFilter:   key,
		LeaseToken:   j.Token,
	})
	j.Group, j.Hash, j.Content = row.GroupID, row.ContentHash, row.Content
	if errors.Is(err, pgx.ErrNoRows) {
		return j, ErrNotFound
	}
	return j, err
}
func (s *Store) finishEmbedding(ctx context.Context, j embeddingJob, v []float32) error {
	if err := validateVector(v, s.Config.Embedding.Dimension); err != nil {
		return err
	}
	rows, err := memorydb.New(s.Pool).FinishEmbedding(ctx, memorydb.FinishEmbeddingParams{
		GroupID:      j.Group,
		ModelVersion: s.Config.ModelVersion(),
		Dimension:    int32(s.Config.Embedding.Dimension),
		ContentHash:  j.Hash,
		LeaseToken:   j.Token,
		Embedding:    vectorText(v),
	})
	if err == nil && rows != 1 {
		return ErrLease
	}
	return err
}
func (s *Store) failEmbedding(ctx context.Context, j embeddingJob) error {
	return memorydb.New(s.Pool).FailEmbedding(ctx, memorydb.FailEmbeddingParams{
		GroupID:      j.Group,
		ModelVersion: s.Config.ModelVersion(),
		Dimension:    int32(s.Config.Embedding.Dimension),
		ContentHash:  j.Hash,
		LeaseToken:   j.Token,
	})
}
func (s *Store) CachedEmbedding(ctx context.Context, embed Embedder, group, text string) ([]float32, error) {
	queries := memorydb.New(s.Pool)
	err := queries.EnsureEmbedding(ctx, memorydb.EnsureEmbeddingParams{
		GroupID:       group,
		ModelVersion:  s.Config.ModelVersion(),
		Dimension:     int32(s.Config.Embedding.Dimension),
		ContentHash:   hash(text),
		Content:       text,
		ModelName:     s.Config.Embedding.Model,
		ModelRevision: s.Config.Embedding.Revision,
	})
	if err != nil {
		return nil, err
	}
	raw, err := queries.ReadEmbedding(ctx, memorydb.ReadEmbeddingParams{
		GroupID:      group,
		ModelVersion: s.Config.ModelVersion(),
		Dimension:    int32(s.Config.Embedding.Dimension),
		ContentHash:  hash(text),
	})
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
