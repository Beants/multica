-- Workspace-scoped template listing (ListWorkflowTemplates). Keep this as
-- the migration's only statement: PostgreSQL rejects CREATE INDEX
-- CONCURRENTLY inside a transaction or multi-command string.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_workflow_template_workspace
    ON workflow_template (workspace_id);
