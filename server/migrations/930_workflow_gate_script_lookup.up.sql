-- Workspace-scoped name uniqueness for workflow_gate_script (security
-- contract #1 allowlist — a node's gate_script_ref must resolve to exactly
-- one row per workspace). Keep this as the migration's only statement:
-- PostgreSQL rejects CREATE INDEX CONCURRENTLY inside a transaction or
-- multi-command string.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS uq_workflow_gate_script_workspace_name
    ON workflow_gate_script (workspace_id, name);
