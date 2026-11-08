-- Legacy text does not establish a structured state or validity period.
ALTER TABLE memory_documents
 ADD COLUMN status text NOT NULL DEFAULT 'unknown' CHECK (status IN ('active','completed','cancelled','unknown')),
 ADD COLUMN valid_from date,
 ADD COLUMN valid_until date,
 ADD COLUMN change_type text NOT NULL DEFAULT 'none' CHECK (change_type IN ('none','supplement','modify','cancel','complete','correct')),
 ADD CONSTRAINT memory_validity_range CHECK (valid_from IS NULL OR valid_until IS NULL OR valid_from <= valid_until),
 ADD CONSTRAINT memory_status_kind CHECK ((status <> 'completed' OR kind IN ('plan','event')) AND (status <> 'cancelled' OR kind IN ('plan','event','decision'))),
 ADD CONSTRAINT memory_change_status CHECK ((change_type <> 'cancel' OR status='cancelled') AND (change_type <> 'complete' OR status='completed'));
-- Re-extract only unfinished legacy checkpoints; retain committed history/cursors.
UPDATE memory_progress SET lease_token=NULL, lease_until=NULL, retry_at=now()
 WHERE group_id IN (SELECT group_id FROM memory_batches WHERE completed_at IS NULL);
DELETE FROM memory_batches WHERE completed_at IS NULL;
