-- Duplicate-activation guard (blueprint §8.2): one row per
-- (run, node, parent step, attempt). NULLS NOT DISTINCT (PG 15+) collapses
-- the always-NULL P0 parent_step_id into a single value — under a plain
-- unique index NULLs stay distinct and duplicate attempts would slip
-- through (precedent: 084_task_usage_dashboard_rollup, as a table
-- constraint; first CONCURRENTLY use here). Keep this as the migration's
-- only statement: PostgreSQL rejects CREATE INDEX CONCURRENTLY inside a
-- transaction or multi-command string.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS uq_step_instance_attempt
    ON step_instance (run_id, node_key, parent_step_id, attempt)
    NULLS NOT DISTINCT;
