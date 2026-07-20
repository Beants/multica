-- Workflow-rule lookups are always workspace-scoped (the CRUD API lists by
-- workspace). Single statement: CONCURRENTLY cannot run inside a transaction
-- or multi-command string (repo migration rule).
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_workflow_rule_workspace
    ON workflow_rule (workspace_id);
