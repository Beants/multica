-- Sweeper / operational queries over finished gate runs only. Partial index
-- (WHERE finished_at IS NOT NULL) keeps 'running' rows out of the index and
-- matches the MVP non-goal of orphan cleanup (P1-3 prd.md Non-Goals).
-- Keep this as the migration's only statement: PostgreSQL rejects CREATE
-- INDEX CONCURRENTLY inside a transaction or multi-command string.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_gate_run_finished
    ON gate_run (finished_at)
    WHERE finished_at IS NOT NULL;
