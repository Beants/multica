-- metrics.sql — P2-3 event_store aggregation queries (dashboard feed).

-- name: AggregateEventsByType :many
-- P2-3 scene-layer metric: event_type distribution for a workspace. Powers
-- the dashboard's "what's happening" breakdown. Whole-window (no since
-- cursor) for MVP — a bounded time-window rollup is follow-up once the
-- dashboard defines its buckets.
SELECT event_type, count(*)::bigint AS event_count
FROM event_store
WHERE workspace_id = $1
GROUP BY event_type
ORDER BY event_count DESC;
