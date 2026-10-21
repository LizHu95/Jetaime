-- Private request evidence, deliberately excluded from messages and agent run APIs.
CREATE TABLE message_client_locations (
 message_id uuid PRIMARY KEY REFERENCES messages(id) ON DELETE CASCADE,
 location jsonb NOT NULL,
 expires_at timestamptz NOT NULL
);
CREATE INDEX message_client_locations_expiry ON message_client_locations(expires_at);
