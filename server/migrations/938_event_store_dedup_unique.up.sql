-- P2-1: dedup_key uniqueness backs the listener's ON CONFLICT DO NOTHING
-- (retries/replays are idempotent). Single statement (CONCURRENTLY rule).
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_event_store_dedup_key
    ON event_store (dedup_key);
