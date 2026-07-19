-- Workspace-scoped hook listing. Keep this as the migration's only
-- statement: PostgreSQL rejects CREATE INDEX CONCURRENTLY inside a
-- transaction or multi-command string.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_workflow_hook_workspace
    ON workflow_hook (workspace_id);
