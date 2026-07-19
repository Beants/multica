-- One row per (workspace, key, version): CreateTemplateVersion's MAX+1 fork
-- turns concurrent-fork losers into a unique-violation error the service
-- retries. Keep this as the migration's only statement: PostgreSQL rejects
-- CREATE INDEX CONCURRENTLY inside a transaction or multi-command string.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS uq_workflow_template_key_version
    ON workflow_template (workspace_id, key, version);
