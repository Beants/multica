-- At most one published version per (workspace, key): publishing vN+1 must
-- first archive the previously published row (service-layer invariant).
-- Keep this as the migration's only statement: PostgreSQL rejects CREATE
-- INDEX CONCURRENTLY inside a transaction or multi-command string.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_workflow_template_one_published
    ON workflow_template (workspace_id, key)
    WHERE status = 'published';
