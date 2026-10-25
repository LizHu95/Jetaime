-- Keys are recorded only for unsolicited proactive replies actually sent.
-- Existing runs remain readable; new policy history begins at this release.
ALTER TABLE agent_runs ADD COLUMN intervention_key text;
CREATE INDEX agent_runs_intervention_history_idx ON agent_runs(group_id,finished_at DESC)
 WHERE trigger_mode='proactive' AND status='succeeded'
 AND response_message_id IS NOT NULL AND intervention_key IS NOT NULL;
