-- Outgoing-edge lookup during template validation and snapshot assembly.
-- Keep this as the migration's only statement: PostgreSQL rejects CREATE
-- INDEX CONCURRENTLY inside a transaction or multi-command string.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_workflow_edge_from
    ON workflow_edge (from_node_id);
