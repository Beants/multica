-- Per-step verdict lookup (run-detail queries). Keep this as the
-- migration's only statement: PostgreSQL rejects CREATE INDEX CONCURRENTLY
-- inside a transaction or multi-command string.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_verdict_step
    ON verdict (step_instance_id);
