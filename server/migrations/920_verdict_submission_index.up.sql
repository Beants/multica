-- One submission yields exactly one verdict. Keep this as the migration's
-- only statement: PostgreSQL rejects CREATE INDEX CONCURRENTLY inside a
-- transaction or multi-command string.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS uq_verdict_submission
    ON verdict (submission_id);
