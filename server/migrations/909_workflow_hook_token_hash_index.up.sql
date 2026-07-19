-- Inbound-hook token resolution hashes the bearer token and probes by
-- token_hash; hashes are unique across all hooks. Keep this as the
-- migration's only statement: PostgreSQL rejects CREATE INDEX CONCURRENTLY
-- inside a transaction or multi-command string.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_workflow_hook_token_hash
    ON workflow_hook (token_hash);
