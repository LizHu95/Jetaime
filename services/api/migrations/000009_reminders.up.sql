-- Each member has one time-clarification draft per conversation.
CREATE TABLE reminder_drafts (
    group_id uuid NOT NULL,
    user_id uuid NOT NULL,
    title text NOT NULL,
    local_date text NOT NULL DEFAULT '',
    local_time text NOT NULL DEFAULT '',
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (group_id,user_id),
    FOREIGN KEY (group_id,user_id) REFERENCES group_members(group_id,user_id)
);

CREATE TABLE reminders (
    id uuid PRIMARY KEY,
    group_id uuid NOT NULL,
    user_id uuid NOT NULL,
    title text NOT NULL CHECK (btrim(title) <> ''),
    due_at timestamptz NOT NULL,
    timezone text NOT NULL DEFAULT 'Asia/Shanghai',
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','sent','cancelled')),
    source_message_id uuid NOT NULL UNIQUE,
    delivery_message_id uuid UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (group_id,user_id) REFERENCES group_members(group_id,user_id),
    FOREIGN KEY (group_id,source_message_id) REFERENCES messages(group_id,id),
    FOREIGN KEY (group_id,delivery_message_id) REFERENCES messages(group_id,id)
);
CREATE INDEX reminders_due_idx ON reminders(due_at) WHERE status='pending';
CREATE INDEX reminders_group_idx ON reminders(group_id,due_at);

-- Cache the actual tool outcome, so worker recovery cannot create twice.
CREATE TABLE reminder_operations (
    message_id uuid PRIMARY KEY REFERENCES messages(id),
    answer text NOT NULL,
    needs_clarification boolean NOT NULL
);

-- Follow-up answers may invoke AI without an explicit mention. The existing
-- composite foreign key still verifies the trigger's real mentions_ai value.
ALTER TABLE agent_runs DROP CONSTRAINT agent_runs_trigger_mentions_ai_check;
