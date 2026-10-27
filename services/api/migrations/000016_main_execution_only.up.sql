-- Retired task modes must use the shared main entry, including failed tasks
-- that can later be retried. Keep message/result kinds as historical facts.
UPDATE agent_runs SET agent_kind='main' WHERE agent_kind IN ('qa','help','extract');
ALTER TABLE agent_runs ALTER COLUMN agent_kind SET DEFAULT 'main';
ALTER TABLE agent_runs DROP CONSTRAINT agent_runs_agent_kind_check;
ALTER TABLE agent_runs ADD CONSTRAINT agent_runs_agent_kind_check
    CHECK (agent_kind IN ('auto','main'));
