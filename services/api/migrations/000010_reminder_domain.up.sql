-- Preserve existing reminders and move drafts into the same lifecycle table.
ALTER TABLE reminders ADD COLUMN local_date text NOT NULL DEFAULT '';
ALTER TABLE reminders ADD COLUMN local_time text NOT NULL DEFAULT '';
ALTER TABLE reminders ADD COLUMN updated_at timestamptz NOT NULL DEFAULT now();
ALTER TABLE reminders ADD COLUMN sent_at timestamptz;
ALTER TABLE reminders ADD COLUMN version bigint NOT NULL DEFAULT 1 CHECK (version>0);
UPDATE reminders SET local_date=to_char(due_at AT TIME ZONE timezone,'YYYY-MM-DD'),
    local_time=to_char(due_at AT TIME ZONE timezone,'HH24:MI'),
    sent_at=CASE WHEN status='sent' THEN (SELECT m.created_at FROM messages m WHERE m.id=reminders.delivery_message_id) ELSE NULL END;
ALTER TABLE reminders ALTER COLUMN due_at DROP NOT NULL;
ALTER TABLE reminders ALTER COLUMN source_message_id DROP NOT NULL;
ALTER TABLE reminders DROP CONSTRAINT reminders_status_check;
ALTER TABLE reminders ADD CONSTRAINT reminders_status_check CHECK (status IN ('draft','pending','sent','cancelled'));
ALTER TABLE reminders ADD CONSTRAINT reminders_scheduled_time_check CHECK (status NOT IN ('pending','sent') OR due_at IS NOT NULL);

INSERT INTO reminders(id,group_id,user_id,title,local_date,local_time,status,updated_at,created_at)
SELECT md5('reminder-draft:'||group_id::text||user_id::text)::uuid,group_id,user_id,title,local_date,local_time,'draft',updated_at,updated_at
FROM reminder_drafts;
CREATE UNIQUE INDEX reminders_active_draft_idx ON reminders(group_id,user_id) WHERE status='draft';
DROP TABLE reminder_drafts;

-- Operations are domain requests; HTTP callers need no AI job or chat message.
ALTER TABLE reminder_operations RENAME COLUMN message_id TO operation_id;
ALTER TABLE reminder_operations DROP CONSTRAINT reminder_operations_message_id_fkey;
ALTER TABLE reminder_operations ADD COLUMN group_id uuid;
ALTER TABLE reminder_operations ADD COLUMN user_id uuid;
ALTER TABLE reminder_operations ADD COLUMN source_message_id uuid REFERENCES messages(id);
ALTER TABLE reminder_operations ADD COLUMN result jsonb;
UPDATE reminder_operations o SET group_id=m.group_id,user_id=m.sender_user_id,source_message_id=m.id,
    result=jsonb_build_object('status',CASE WHEN o.needs_clarification THEN 'needs_clarification' ELSE 'completed' END,'legacyAnswer',o.answer)
FROM messages m WHERE m.id=o.operation_id;
ALTER TABLE reminder_operations ALTER COLUMN group_id SET NOT NULL;
ALTER TABLE reminder_operations ALTER COLUMN user_id SET NOT NULL;
ALTER TABLE reminder_operations ALTER COLUMN result SET NOT NULL;
ALTER TABLE reminder_operations ADD FOREIGN KEY (group_id,user_id) REFERENCES group_members(group_id,user_id);
ALTER TABLE reminder_operations DROP COLUMN answer;
ALTER TABLE reminder_operations DROP COLUMN needs_clarification;
