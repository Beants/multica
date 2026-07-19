-- Per-template run history. Keep this as the migration's only statement:
-- PostgreSQL rejects CREATE INDEX CONCURRENTLY inside a transaction or
-- multi-command string.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_workflow_run_template
    ON workflow_run (template_id);
