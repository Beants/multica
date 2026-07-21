-- P2-1: every dashboard query filters by workspace, newest-first. Single
-- statement: CONCURRENTLY cannot run inside a transaction or multi-command
-- string (repo migration rule).
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_event_store_workspace_occurred
    ON event_store (workspace_id, occurred_at DESC);
