-- Lookup of every gate_run for a given step_instance (run-detail timeline,
-- rework history). Keep this as the migration's only statement: PostgreSQL
-- rejects CREATE INDEX CONCURRENTLY inside a transaction or multi-command
-- string.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_gate_run_step
    ON gate_run (step_instance_id);
