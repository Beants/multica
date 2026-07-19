-- Inbound-hook idempotency (blueprint §8.2): re-pushed deliveries of the
-- same external work collide on (workspace, source_type, source_id,
-- template) and the engine returns the existing run. source_id stays NULL
-- for manual runs; NULLs remain distinct under a plain unique index, so
-- manual runs never dedupe (desired). Keep this as the migration's only
-- statement: PostgreSQL rejects CREATE INDEX CONCURRENTLY inside a
-- transaction or multi-command string.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS uq_workflow_run_source
    ON workflow_run (workspace_id, source_type, source_id, template_id);
