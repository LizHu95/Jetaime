-- pgvector must be installed on the database host; migrations never recreate the DB.
CREATE EXTENSION IF NOT EXISTS vector WITH SCHEMA public;

CREATE TABLE memory_progress (
 group_id uuid PRIMARY KEY REFERENCES chat_groups(id),
 pipeline text NOT NULL,
 cursor_seq bigint NOT NULL DEFAULT 0,
 backfill_end_seq bigint NOT NULL,
 lease_token uuid,
 lease_until timestamptz,
 retry_at timestamptz NOT NULL DEFAULT now(),
 attempts integer NOT NULL DEFAULT 0,
 last_error text,
 updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK (cursor_seq >= 0 AND backfill_end_seq >= 0)
);
CREATE TABLE memory_batches (
 group_id uuid NOT NULL REFERENCES chat_groups(id),
 from_seq bigint NOT NULL,
 to_seq bigint NOT NULL,
 candidates jsonb NOT NULL,
 mutations jsonb NOT NULL DEFAULT '[]'::jsonb,
 completed_at timestamptz,
 PRIMARY KEY (group_id,from_seq),
 CHECK (to_seq > from_seq)
);
CREATE TABLE memory_documents (
 id uuid PRIMARY KEY,
 group_id uuid NOT NULL REFERENCES chat_groups(id),
 logical_id uuid NOT NULL,
 revision integer NOT NULL CHECK (revision > 0),
 kind text NOT NULL CHECK (kind IN ('history','preference','decision','plan','fact','constraint','event')),
 subject text NOT NULL,
 event_key text NOT NULL,
 temporal_scope text NOT NULL CHECK (temporal_scope IN ('enduring','temporary','event')),
 content text NOT NULL CHECK (btrim(content) <> ''),
 content_hash text NOT NULL,
 active boolean NOT NULL DEFAULT true,
 change_action text NOT NULL CHECK (change_action IN ('ADD','UPDATE','INVALIDATE')),
 source_start_at timestamptz NOT NULL,
 source_end_at timestamptz NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE (group_id,id),
 UNIQUE (group_id,logical_id,revision)
);
CREATE UNIQUE INDEX memory_documents_current ON memory_documents(group_id,logical_id) WHERE active;
CREATE INDEX memory_documents_filter ON memory_documents(group_id,kind,subject) WHERE active;
CREATE TABLE memory_document_sources (
 group_id uuid NOT NULL,
 document_id uuid NOT NULL,
 message_id uuid NOT NULL,
 PRIMARY KEY (document_id,message_id),
 FOREIGN KEY (group_id,document_id) REFERENCES memory_documents(group_id,id) ON DELETE CASCADE,
 FOREIGN KEY (group_id,message_id) REFERENCES messages(group_id,id) ON DELETE CASCADE
);
CREATE INDEX memory_sources_message ON memory_document_sources(group_id,message_id);
CREATE TABLE memory_embeddings (
 group_id uuid NOT NULL REFERENCES chat_groups(id),
 model_version text NOT NULL,
 model_name text NOT NULL,
 model_revision text NOT NULL,
 dimension integer NOT NULL CHECK (dimension BETWEEN 1 AND 16000),
 content_hash text NOT NULL,
 content text NOT NULL,
 embedding public.vector,
 status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','running','ready')),
 lease_token uuid,
 lease_until timestamptz,
 retry_at timestamptz NOT NULL DEFAULT now(),
 attempts integer NOT NULL DEFAULT 0,
 last_error text,
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY (group_id,model_version,dimension,content_hash),
 CHECK ((status='ready' AND embedding IS NOT NULL AND public.vector_dims(embedding)=dimension)
     OR (status<>'ready' AND embedding IS NULL))
);
CREATE INDEX memory_embeddings_pending ON memory_embeddings(retry_at) WHERE status<>'ready';

-- Editing/deleting evidence withdraws every derived version and its vector.
-- Existing product has immutable membership and closes the group instead of leaving.
CREATE FUNCTION memory_withdraw_evidence() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 DELETE FROM memory_embeddings e WHERE e.group_id=OLD.group_id AND
   (e.content_hash IN (SELECT d.content_hash FROM memory_documents d
     JOIN memory_document_sources s ON s.document_id=d.id WHERE s.group_id=OLD.group_id AND s.message_id=OLD.id)
    OR NOT EXISTS (SELECT 1 FROM memory_documents d WHERE d.group_id=e.group_id AND d.content_hash=e.content_hash));
 DELETE FROM memory_documents WHERE group_id=OLD.group_id AND id IN
   (SELECT document_id FROM memory_document_sources WHERE group_id=OLD.group_id AND message_id=OLD.id);
 DELETE FROM memory_batches WHERE group_id=OLD.group_id;
 UPDATE memory_progress SET lease_token=NULL,lease_until=NULL,retry_at=now() WHERE group_id=OLD.group_id;
 RETURN OLD;
END;
$$;
CREATE TRIGGER memory_source_delete BEFORE DELETE ON messages
 FOR EACH ROW EXECUTE FUNCTION memory_withdraw_evidence();
CREATE FUNCTION memory_reject_source_edit() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF (NEW.content,NEW.sender_user_id,NEW.created_at,NEW.group_id,NEW.seq) IS DISTINCT FROM
    (OLD.content,OLD.sender_user_id,OLD.created_at,OLD.group_id,OLD.seq) THEN
   RAISE EXCEPTION 'message edits require an explicit memory reindex workflow' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER memory_source_edit BEFORE UPDATE ON messages
 FOR EACH ROW EXECUTE FUNCTION memory_reject_source_edit();
CREATE FUNCTION memory_close_group() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.status='closed' AND OLD.status<>'closed' THEN
   DELETE FROM memory_documents WHERE group_id=NEW.id;
   DELETE FROM memory_embeddings WHERE group_id=NEW.id;
   DELETE FROM memory_batches WHERE group_id=NEW.id;
   DELETE FROM memory_progress WHERE group_id=NEW.id;
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER memory_group_close AFTER UPDATE ON chat_groups
 FOR EACH ROW EXECUTE FUNCTION memory_close_group();

-- Serialize derived writes with closure, including INSERT ... SELECT snapshots.
CREATE TRIGGER memory_documents_active BEFORE INSERT ON memory_documents
 FOR EACH ROW EXECUTE FUNCTION guard_active_group_insert();
CREATE TRIGGER memory_embeddings_active BEFORE INSERT ON memory_embeddings
 FOR EACH ROW EXECUTE FUNCTION guard_active_group_insert();
CREATE TRIGGER memory_batches_active BEFORE INSERT ON memory_batches
 FOR EACH ROW EXECUTE FUNCTION guard_active_group_insert();
CREATE TRIGGER memory_progress_active BEFORE INSERT ON memory_progress
 FOR EACH ROW EXECUTE FUNCTION guard_active_group_insert();
