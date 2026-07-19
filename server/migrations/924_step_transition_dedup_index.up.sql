-- Retried transition writes collide on (step, from, to, attempt) and the
-- writer's ON CONFLICT DO NOTHING turns them into no-ops instead of
-- duplicating history (circuit-breaker counting reads this table). Keep
-- this as the migration's only statement: PostgreSQL rejects CREATE INDEX
-- CONCURRENTLY inside a transaction or multi-command string.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_step_transition_dedup
    ON step_transition (step_instance_id, from_status, to_status, attempt);
