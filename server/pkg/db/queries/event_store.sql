-- event_store.sql — P2-1 global event log queries.

-- name: InsertEvent :one
-- Append one event. ON CONFLICT (dedup_key) DO UPDATE refreshes occurred_at
-- (a retry/replay = "this event happened again just now") and always returns
-- a row, so the :one listener doesn't error on the conflict path. The unique
-- index (938) keeps the store deduped to one row per dedup_key.
INSERT INTO event_store (workspace_id, event_type, actor_type, actor_id, payload, dedup_key)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (dedup_key) DO UPDATE SET occurred_at = now()
RETURNING *;

-- name: ListEvents :many
-- P2-1 query API + P2-4 dashboard feed. Filters by workspace (required) +
-- optional event_type; newest-first paging via limit.
SELECT * FROM event_store
WHERE workspace_id = $1
  AND ($2::text = '' OR event_type = $2)
ORDER BY occurred_at DESC
LIMIT $3;
