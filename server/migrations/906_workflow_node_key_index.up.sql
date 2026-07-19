-- node_key is unique within a template (snapshot assembly and edge
-- resolution address nodes by key). Keep this as the migration's only
-- statement: PostgreSQL rejects CREATE INDEX CONCURRENTLY inside a
-- transaction or multi-command string.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS uq_workflow_node_key
    ON workflow_node (template_id, node_key);
