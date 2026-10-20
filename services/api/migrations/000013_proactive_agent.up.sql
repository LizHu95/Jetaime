ALTER TABLE chat_groups ADD COLUMN proactive_enabled boolean NOT NULL DEFAULT false;
-- The deadline is reset by human messages, never by assistant messages.
ALTER TABLE chat_groups ADD COLUMN proactive_message_id uuid;
ALTER TABLE chat_groups ADD COLUMN proactive_due_at timestamptz;
ALTER TABLE chat_groups ADD FOREIGN KEY (id, proactive_message_id) REFERENCES messages(group_id, id);
CREATE INDEX chat_groups_proactive_due_idx ON chat_groups(proactive_due_at) WHERE proactive_due_at IS NOT NULL;

ALTER TABLE agent_runs ADD COLUMN trigger_mode text NOT NULL DEFAULT 'mention'
    CHECK (trigger_mode IN ('mention', 'proactive'));
ALTER TABLE agent_runs ADD COLUMN recognition jsonb;
-- A successful proactive assessment can intentionally produce no message.
DO $$ DECLARE constraint_name text; BEGIN
    FOR constraint_name IN SELECT conname FROM pg_constraint
        WHERE conrelid='agent_runs'::regclass AND contype='c'
        AND pg_get_constraintdef(oid) LIKE '%response_message_id%'
    LOOP
        EXECUTE format('ALTER TABLE agent_runs DROP CONSTRAINT %I', constraint_name);
    END LOOP;
END $$;
ALTER TABLE agent_runs ADD CONSTRAINT agent_runs_response_check CHECK (
    (status='succeeded' AND (response_message_id IS NOT NULL OR trigger_mode='proactive'))
    OR (status<>'succeeded' AND response_message_id IS NULL)
);
