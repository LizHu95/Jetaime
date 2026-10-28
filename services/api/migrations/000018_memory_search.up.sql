-- Install pg_search >= 0.26.0 on the PostgreSQL host before running migrations.
-- Keep the extension in its own schemas (pdb/paradedb), including in isolated test schemas.
CREATE EXTENSION IF NOT EXISTS pg_search;
DO $$
DECLARE
 search_version text;
BEGIN
 SELECT extversion INTO search_version FROM pg_extension WHERE extname = 'pg_search';
 IF string_to_array(split_part(search_version, '-', 1), '.')::int[] < ARRAY[0,26,0] THEN
  RAISE EXCEPTION 'memory search requires pg_search >= 0.26.0; installed: %', search_version;
 END IF;
END;
$$;

-- Chinese memories need word segmentation; include scope/filter columns so search can narrow candidates.
-- Only current documents participate; historical revisions remain in the table for provenance.
CREATE INDEX memory_documents_search ON memory_documents
 USING paradedb (id, group_id, (content::pdb.jieba), kind, subject, source_start_at, source_end_at)
 WHERE active;
