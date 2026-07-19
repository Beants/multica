-- Workspace run listing ordered newest-first (ListWorkflowRuns). Keep this
-- as the migration's only statement: PostgreSQL rejects CREATE INDEX
-- CONCURRENTLY inside a transaction or multi-command string.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_workflow_run_workspace
    ON workflow_run (workspace_id, started_at DESC);
