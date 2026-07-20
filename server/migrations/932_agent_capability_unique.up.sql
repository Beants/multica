-- One capability row per (agent, capability_key): the P1-7 management API
-- UPSERT relies on this to dedupe declarations, and the matcher's GROUP BY
-- agent_id counting assumes no duplicates. Single statement: PostgreSQL
-- rejects CREATE INDEX CONCURRENTLY inside a transaction or multi-command
-- string (repo migration rule).
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_agent_capability_agent_key
    ON agent_capability (agent_id, capability_key);
