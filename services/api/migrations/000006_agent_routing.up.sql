-- Keep explicit legacy qa/extract requests while new @AI requests use auto.
ALTER TABLE messages DROP CONSTRAINT messages_agent_kind_check;
ALTER TABLE messages ADD CONSTRAINT messages_agent_kind_check
    CHECK (agent_kind IN ('auto','help','qa','extract'));
ALTER TABLE agent_runs DROP CONSTRAINT agent_runs_agent_kind_check;
ALTER TABLE agent_runs ADD CONSTRAINT agent_runs_agent_kind_check
    CHECK (agent_kind IN ('auto','help','qa','extract'));
