-- One submission per step attempt (step_instance is already per-attempt,
-- so no attempt column in the key — design.md §7 deviation #7). Keep this
-- as the migration's only statement: PostgreSQL rejects CREATE INDEX
-- CONCURRENTLY inside a transaction or multi-command string.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS uq_submission_step
    ON submission (step_instance_id);
