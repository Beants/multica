-- daemon/CLI retry safety (blueprint §8.2): re-posts with a stable caller
-- key collide instead of duplicating. Scoped per step: different steps may
-- legitimately reuse the same caller key, so a global unique would
-- false-collide. Keep this as the migration's only statement: PostgreSQL
-- rejects CREATE INDEX CONCURRENTLY inside a transaction or multi-command
-- string.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_submission_idempotency_key
    ON submission (step_instance_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;
