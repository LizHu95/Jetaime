-- Persist the actual execution trace separately from the enqueue propagation context.
ALTER TABLE agent_runs ADD COLUMN trace_id text
    CHECK (trace_id IS NULL OR trace_id ~ '^[0-9a-f]{32}$');
