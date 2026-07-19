-- Per-run acceptance listing (run detail + rejection circuit-breaker
-- count). Keep this as the migration's only statement: PostgreSQL rejects
-- CREATE INDEX CONCURRENTLY inside a transaction or multi-command string.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_acceptance_run
    ON acceptance (run_id);
