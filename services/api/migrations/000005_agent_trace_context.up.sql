ALTER TABLE agent_runs
 ADD COLUMN trace_parent text NOT NULL DEFAULT '',
 ADD COLUMN trace_state text NOT NULL DEFAULT '',
 ADD COLUMN queued_at timestamptz NOT NULL DEFAULT now();
