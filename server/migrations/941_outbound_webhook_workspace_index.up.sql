-- P2-2: list a workspace's webhook configs + look up deliveries by webhook.
-- Single statement (CONCURRENTLY rule).
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_outbound_webhook_workspace
    ON outbound_webhook (workspace_id);
