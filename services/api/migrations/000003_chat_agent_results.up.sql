-- Reuse the Agent's qa/extract result while keeping ordinary messages unchanged.
ALTER TABLE messages ADD COLUMN agent_kind text NOT NULL DEFAULT 'qa'
    CHECK (agent_kind IN ('qa','extract'));
ALTER TABLE messages ADD COLUMN agent_result jsonb;
ALTER TABLE messages ADD CONSTRAINT messages_agent_result_sender
    CHECK (agent_result IS NULL OR (sender_type = 'assistant' AND jsonb_typeof(agent_result) = 'object'));
ALTER TABLE agent_runs ADD COLUMN agent_kind text NOT NULL DEFAULT 'qa'
    CHECK (agent_kind IN ('qa','extract'));
