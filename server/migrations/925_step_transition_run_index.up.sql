-- Run-detail timeline in write order. Keep this as the migration's only
-- statement: PostgreSQL rejects CREATE INDEX CONCURRENTLY inside a
-- transaction or multi-command string.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_step_transition_run
    ON step_transition (run_id, created_at);
