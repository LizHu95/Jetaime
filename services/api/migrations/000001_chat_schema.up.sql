-- Design baseline: fixed two-person conversations with one shared assistant.
-- UUIDs are supplied by Go; no PostgreSQL extensions are required.
-- The migration runner owns the transaction. For manual checks use psql -1.

CREATE TABLE users (
    id uuid PRIMARY KEY,
    nickname text NOT NULL CHECK (char_length(btrim(nickname)) BETWEEN 1 AND 24),
    avatar_url text,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE user_credentials (
    user_id uuid PRIMARY KEY REFERENCES users(id),
    username text NOT NULL CHECK (char_length(btrim(username)) BETWEEN 2 AND 24),
    password_hash text NOT NULL CHECK (password_hash <> ''),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX user_credentials_username_unique ON user_credentials (lower(username));

CREATE TABLE auth_sessions (
    id uuid PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users(id),
    token_hash bytea NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    CHECK (expires_at > created_at),
    CHECK (revoked_at IS NULL OR revoked_at >= created_at)
);
CREATE INDEX auth_sessions_user_idx ON auth_sessions (user_id);
CREATE INDEX auth_sessions_expiry_idx ON auth_sessions (expires_at);

CREATE TABLE chat_groups (
    id uuid PRIMARY KEY,
    user_low_id uuid NOT NULL REFERENCES users(id),
    user_high_id uuid NOT NULL REFERENCES users(id),
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'closed')),
    last_message_seq bigint NOT NULL DEFAULT 0 CHECK (last_message_seq >= 0),
    last_message_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    closed_at timestamptz,
    closed_by uuid REFERENCES users(id),
    CHECK (user_low_id < user_high_id),
    CHECK (closed_by IS NULL OR closed_by IN (user_low_id, user_high_id)),
    CHECK ((status = 'active' AND closed_at IS NULL AND closed_by IS NULL)
        OR (status = 'closed' AND closed_at IS NOT NULL AND closed_by IS NOT NULL)),
    UNIQUE (id, user_low_id, user_high_id)
);
CREATE UNIQUE INDEX chat_groups_active_pair_unique
    ON chat_groups (user_low_id, user_high_id) WHERE status = 'active';

CREATE TABLE group_members (
    group_id uuid NOT NULL REFERENCES chat_groups(id),
    user_id uuid NOT NULL REFERENCES users(id),
    last_read_seq bigint NOT NULL DEFAULT 0 CHECK (last_read_seq >= 0),
    joined_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (group_id, user_id)
);
CREATE INDEX group_members_user_idx ON group_members (user_id, group_id);

CREATE TABLE group_assistants (
    id uuid PRIMARY KEY,
    group_id uuid NOT NULL UNIQUE REFERENCES chat_groups(id),
    name text NOT NULL DEFAULT 'AI 助手' CHECK (btrim(name) <> ''),
    agent_key text NOT NULL CHECK (btrim(agent_key) <> ''),
    enabled boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (group_id, id)
);

-- Recommendation: accept a contact request before creating the conversation.
CREATE TABLE contact_requests (
    id uuid PRIMARY KEY,
    from_user_id uuid NOT NULL REFERENCES users(id),
    to_user_id uuid NOT NULL REFERENCES users(id),
    user_low_id uuid GENERATED ALWAYS AS (least(from_user_id, to_user_id)) STORED,
    user_high_id uuid GENERATED ALWAYS AS (greatest(from_user_id, to_user_id)) STORED,
    status text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'accepted', 'rejected', 'cancelled')),
    group_id uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    resolved_at timestamptz,
    CHECK (from_user_id <> to_user_id),
    CHECK ((status = 'pending' AND resolved_at IS NULL)
        OR (status <> 'pending' AND resolved_at IS NOT NULL)),
    CHECK ((status = 'accepted' AND group_id IS NOT NULL)
        OR (status <> 'accepted' AND group_id IS NULL)),
    FOREIGN KEY (group_id, user_low_id, user_high_id)
        REFERENCES chat_groups(id, user_low_id, user_high_id)
);
CREATE UNIQUE INDEX contact_requests_pending_pair_unique
    ON contact_requests (user_low_id, user_high_id) WHERE status = 'pending';
CREATE INDEX contact_requests_inbox_idx ON contact_requests (to_user_id, created_at DESC)
    WHERE status = 'pending';
CREATE INDEX contact_requests_outbox_idx ON contact_requests (from_user_id, created_at DESC)
    WHERE status = 'pending';

CREATE TABLE messages (
    id uuid PRIMARY KEY,
    group_id uuid NOT NULL REFERENCES chat_groups(id),
    seq bigint NOT NULL CHECK (seq > 0),
    sender_type text NOT NULL CHECK (sender_type IN ('user', 'assistant')),
    sender_user_id uuid,
    sender_assistant_id uuid,
    content text NOT NULL CHECK (btrim(content) <> ''),
    mentions_ai boolean NOT NULL DEFAULT false,
    client_message_id uuid,
    reply_to_id uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((sender_type = 'user' AND sender_user_id IS NOT NULL
                AND sender_assistant_id IS NULL AND client_message_id IS NOT NULL)
        OR (sender_type = 'assistant' AND sender_user_id IS NULL
                AND sender_assistant_id IS NOT NULL AND client_message_id IS NULL
                AND mentions_ai = false)),
    UNIQUE (group_id, seq),
    UNIQUE (group_id, id),
    UNIQUE (id, group_id, seq, sender_user_id, mentions_ai),
    UNIQUE (id, group_id, sender_assistant_id),
    FOREIGN KEY (group_id, sender_user_id) REFERENCES group_members(group_id, user_id),
    FOREIGN KEY (group_id, sender_assistant_id) REFERENCES group_assistants(group_id, id),
    FOREIGN KEY (group_id, reply_to_id) REFERENCES messages(group_id, id)
);
CREATE UNIQUE INDEX messages_client_unique ON messages (sender_user_id, client_message_id)
    WHERE sender_type = 'user';

CREATE TABLE agent_runs (
    id uuid PRIMARY KEY,
    group_id uuid NOT NULL REFERENCES chat_groups(id),
    assistant_id uuid NOT NULL,
    trigger_message_id uuid NOT NULL,
    requested_by uuid NOT NULL,
    context_end_seq bigint NOT NULL CHECK (context_end_seq > 0),
    trigger_mentions_ai boolean NOT NULL DEFAULT true CHECK (trigger_mentions_ai = true),
    status text NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'cancelled')),
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    lease_expires_at timestamptz,
    response_message_id uuid UNIQUE,
    error_code text,
    created_at timestamptz NOT NULL DEFAULT now(),
    started_at timestamptz,
    finished_at timestamptz,
    UNIQUE (trigger_message_id),
    FOREIGN KEY (group_id, assistant_id) REFERENCES group_assistants(group_id, id),
    FOREIGN KEY (group_id, requested_by) REFERENCES group_members(group_id, user_id),
    FOREIGN KEY (trigger_message_id, group_id, context_end_seq, requested_by, trigger_mentions_ai)
        REFERENCES messages(id, group_id, seq, sender_user_id, mentions_ai),
    FOREIGN KEY (response_message_id, group_id, assistant_id)
        REFERENCES messages(id, group_id, sender_assistant_id),
    CHECK ((status = 'succeeded' AND response_message_id IS NOT NULL)
        OR (status <> 'succeeded' AND response_message_id IS NULL)),
    CHECK ((status IN ('queued', 'running') AND finished_at IS NULL)
        OR (status IN ('succeeded', 'failed', 'cancelled') AND finished_at IS NOT NULL)),
    CHECK ((status = 'running' AND started_at IS NOT NULL AND lease_expires_at IS NOT NULL)
        OR (status <> 'running' AND lease_expires_at IS NULL))
);
CREATE INDEX agent_runs_queue_idx ON agent_runs (created_at) WHERE status = 'queued';
CREATE INDEX agent_runs_lease_idx ON agent_runs (lease_expires_at) WHERE status = 'running';
CREATE INDEX agent_runs_group_idx ON agent_runs (group_id, created_at DESC);

-- Members and assistants are created with the group and keep their identities.
CREATE FUNCTION guard_group_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF (NEW.id, NEW.user_low_id, NEW.user_high_id)
        IS DISTINCT FROM (OLD.id, OLD.user_low_id, OLD.user_high_id) THEN
        RAISE EXCEPTION 'conversation identity is immutable' USING ERRCODE = '23514';
    END IF;
    IF OLD.status = 'closed' AND NEW.status <> 'closed' THEN
        RAISE EXCEPTION 'closed conversation cannot reopen' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER chat_groups_identity BEFORE UPDATE ON chat_groups
    FOR EACH ROW EXECUTE FUNCTION guard_group_identity();

CREATE FUNCTION guard_group_member() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    g chat_groups%ROWTYPE;
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'close the conversation instead of removing a member' USING ERRCODE = '23514';
    END IF;
    IF TG_OP = 'UPDATE' AND (NEW.group_id, NEW.user_id, NEW.joined_at)
        IS DISTINCT FROM (OLD.group_id, OLD.user_id, OLD.joined_at) THEN
        RAISE EXCEPTION 'member identity is immutable' USING ERRCODE = '23514';
    END IF;
    SELECT * INTO g FROM chat_groups WHERE id = NEW.group_id FOR UPDATE;
    IF NOT FOUND OR NEW.user_id NOT IN (g.user_low_id, g.user_high_id) THEN
        RAISE EXCEPTION 'user is not a participant' USING ERRCODE = '23514';
    END IF;
    IF g.status <> 'active' THEN
        RAISE EXCEPTION 'conversation is closed' USING ERRCODE = '23514';
    END IF;
    IF NEW.last_read_seq > g.last_message_seq THEN
        RAISE EXCEPTION 'read cursor exceeds message sequence' USING ERRCODE = '23514';
    END IF;
    IF TG_OP = 'UPDATE' AND NEW.last_read_seq < OLD.last_read_seq THEN
        RAISE EXCEPTION 'read cursor cannot move backwards' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER group_members_guard BEFORE INSERT OR UPDATE OR DELETE ON group_members
    FOR EACH ROW EXECUTE FUNCTION guard_group_member();

CREATE FUNCTION guard_assistant_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'disable the assistant instead of removing it' USING ERRCODE = '23514';
    END IF;
    IF (NEW.id, NEW.group_id) IS DISTINCT FROM (OLD.id, OLD.group_id) THEN
        RAISE EXCEPTION 'assistant identity is immutable' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER group_assistants_identity BEFORE UPDATE OR DELETE ON group_assistants
    FOR EACH ROW EXECUTE FUNCTION guard_assistant_identity();

-- Deferred: the creation transaction must contain two members and one assistant.
CREATE FUNCTION check_group_complete() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF (SELECT count(*) FROM group_members WHERE group_id = NEW.id) <> 2
        OR (SELECT count(*) FROM group_assistants WHERE group_id = NEW.id) <> 1 THEN
        RAISE EXCEPTION 'conversation requires two members and one assistant' USING ERRCODE = '23514';
    END IF;
    RETURN NULL;
END;
$$;
CREATE CONSTRAINT TRIGGER chat_groups_complete AFTER INSERT ON chat_groups
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_group_complete();

-- Backstop against late replies: inserts serialize with conversation closure.
CREATE FUNCTION guard_active_group_insert() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    group_status text;
BEGIN
    SELECT status INTO group_status FROM chat_groups WHERE id = NEW.group_id FOR UPDATE;
    IF NOT FOUND OR group_status <> 'active' THEN
        RAISE EXCEPTION 'conversation is closed or missing' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER messages_active_group BEFORE INSERT ON messages
    FOR EACH ROW EXECUTE FUNCTION guard_active_group_insert();
CREATE TRIGGER agent_runs_active_group BEFORE INSERT ON agent_runs
    FOR EACH ROW EXECUTE FUNCTION guard_active_group_insert();
