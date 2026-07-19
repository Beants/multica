-- Issue-scoped step lookups. Partial because most steps link no issue.
-- Keep this as the migration's only statement: PostgreSQL rejects CREATE
-- INDEX CONCURRENTLY inside a transaction or multi-command string.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_step_instance_issue
    ON step_instance (issue_id)
    WHERE issue_id IS NOT NULL;
