-- Main Agent answers directly; legacy explicit requests remain compatible.
ALTER TABLE messages DROP CONSTRAINT messages_agent_kind_check;
ALTER TABLE messages ADD CONSTRAINT messages_agent_kind_check
    CHECK (agent_kind IN ('auto','main','help','qa','extract'));
ALTER TABLE agent_runs DROP CONSTRAINT agent_runs_agent_kind_check;
ALTER TABLE agent_runs ADD CONSTRAINT agent_runs_agent_kind_check
    CHECK (agent_kind IN ('auto','main','help','qa','extract'));
