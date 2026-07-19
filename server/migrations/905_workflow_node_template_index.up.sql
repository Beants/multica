-- Per-template node listing (template detail view). Keep this as the
-- migration's only statement: PostgreSQL rejects CREATE INDEX CONCURRENTLY
-- inside a transaction or multi-command string.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_workflow_node_template
    ON workflow_node (template_id);
