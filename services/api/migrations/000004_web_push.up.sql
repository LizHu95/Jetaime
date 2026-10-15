CREATE TABLE web_push_subscriptions (
    id uuid PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users(id),
    session_id uuid NOT NULL REFERENCES auth_sessions(id),
    endpoint text NOT NULL UNIQUE,
    p256dh text NOT NULL,
    auth text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    last_test_at timestamptz
);
CREATE INDEX web_push_subscriptions_user_idx ON web_push_subscriptions(user_id);

CREATE TABLE web_push_jobs (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    subscription_id uuid NOT NULL REFERENCES web_push_subscriptions(id) ON DELETE CASCADE,
    message_id uuid REFERENCES messages(id) ON DELETE CASCADE,
    attempts integer NOT NULL DEFAULT 0,
    available_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL DEFAULT now() + interval '1 hour',
    completed_at timestamptz,
    last_status integer,
    UNIQUE(subscription_id, message_id)
);
CREATE INDEX web_push_jobs_pending_idx ON web_push_jobs(available_at) WHERE completed_at IS NULL;

-- The outbox commits with the message, including AI answers and idempotent sends.
CREATE FUNCTION enqueue_web_push() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO web_push_jobs(subscription_id, message_id)
    SELECT s.id, NEW.id FROM web_push_subscriptions s
    JOIN auth_sessions a ON a.id=s.session_id AND a.user_id=s.user_id
    JOIN group_members m ON m.user_id=s.user_id AND m.group_id=NEW.group_id
    WHERE a.revoked_at IS NULL AND a.expires_at>now()
      AND s.user_id IS DISTINCT FROM NEW.sender_user_id;
    RETURN NEW;
END;
$$;
CREATE TRIGGER messages_web_push AFTER INSERT ON messages
    FOR EACH ROW EXECUTE FUNCTION enqueue_web_push();
