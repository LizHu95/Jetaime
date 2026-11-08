-- name: RepairEmbeddingQueue :exec
INSERT INTO memory_embeddings (
    group_id, model_version, dimension, content_hash, content, model_name, model_revision
)
SELECT DISTINCT
    d.group_id,
    sqlc.arg(model_version)::text,
    sqlc.arg(dimension)::integer,
    d.content_hash,
    d.content,
    sqlc.arg(model_name)::text,
    sqlc.arg(model_revision)::text
FROM memory_documents AS d
JOIN chat_groups AS g ON g.id = d.group_id
JOIN group_assistants AS a ON a.group_id = g.id
WHERE d.active AND g.status = 'active' AND a.enabled
ON CONFLICT DO NOTHING;

-- name: ClaimEmbedding :one
-- Atomically lease one eligible task; concurrent workers skip locked tasks.
WITH picked AS (
    SELECT e.group_id, e.content_hash
    FROM memory_embeddings AS e
    JOIN chat_groups AS g ON g.id = e.group_id
    JOIN group_assistants AS a ON a.group_id = g.id
    WHERE e.model_version = sqlc.arg(model_version)
      AND e.dimension = sqlc.arg(dimension)
      AND g.status = 'active'
      AND a.enabled
      AND e.status <> 'ready'
      AND e.retry_at <= now()
      AND (e.lease_until IS NULL OR e.lease_until < now())
      AND (sqlc.arg(group_filter)::text = '' OR e.group_id::text = sqlc.arg(group_filter)::text)
      AND (sqlc.arg(hash_filter)::text = '' OR e.content_hash = sqlc.arg(hash_filter)::text)
    ORDER BY e.updated_at
    FOR UPDATE OF e SKIP LOCKED
    LIMIT 1
)
UPDATE memory_embeddings AS e
SET status = 'running',
    lease_token = sqlc.arg(lease_token)::text::uuid,
    lease_until = now() + interval '2 minutes',
    attempts = e.attempts + 1
FROM picked
WHERE e.group_id = picked.group_id
  AND e.content_hash = picked.content_hash
  AND e.model_version = sqlc.arg(model_version)
  AND e.dimension = sqlc.arg(dimension)
RETURNING e.group_id::text AS group_id, e.content_hash, e.content;

-- name: FinishEmbedding :execrows
-- A stale worker must not publish a result after its lease expires.
UPDATE memory_embeddings AS e
SET status = 'ready',
    embedding = sqlc.arg(embedding)::text::public.vector,
    lease_until = NULL,
    lease_token = NULL,
    last_error = NULL,
    updated_at = now()
FROM chat_groups AS g, group_assistants AS a
WHERE e.group_id = sqlc.arg(group_id)::text::uuid
  AND e.model_version = sqlc.arg(model_version)
  AND e.dimension = sqlc.arg(dimension)
  AND e.content_hash = sqlc.arg(content_hash)
  AND e.lease_token = sqlc.arg(lease_token)::text::uuid
  AND e.lease_until > now()
  AND g.id = e.group_id
  AND g.status = 'active'
  AND a.group_id = g.id
  AND a.enabled;

-- name: FailEmbedding :exec
UPDATE memory_embeddings
SET status = 'pending',
    lease_until = NULL,
    lease_token = NULL,
    last_error = 'embedding_failed',
    retry_at = now() + interval '30 seconds' * LEAST(attempts, 20),
    updated_at = now()
WHERE group_id = sqlc.arg(group_id)::text::uuid
  AND model_version = sqlc.arg(model_version)
  AND dimension = sqlc.arg(dimension)
  AND content_hash = sqlc.arg(content_hash)
  AND lease_token = sqlc.arg(lease_token)::text::uuid;

-- name: EnsureEmbedding :exec
INSERT INTO memory_embeddings (
    group_id, model_version, dimension, content_hash, content, model_name, model_revision
)
SELECT
    g.id,
    sqlc.arg(model_version),
    sqlc.arg(dimension),
    sqlc.arg(content_hash),
    sqlc.arg(content),
    sqlc.arg(model_name),
    sqlc.arg(model_revision)
FROM chat_groups AS g
JOIN group_assistants AS a ON a.group_id = g.id
WHERE g.id = sqlc.arg(group_id)::text::uuid
  AND g.status = 'active'
  AND a.enabled
ON CONFLICT DO NOTHING;

-- name: ReadEmbedding :one
SELECT embedding::text
FROM memory_embeddings
WHERE group_id = sqlc.arg(group_id)::text::uuid
  AND model_version = sqlc.arg(model_version)
  AND dimension = sqlc.arg(dimension)
  AND content_hash = sqlc.arg(content_hash)
  AND status = 'ready';
