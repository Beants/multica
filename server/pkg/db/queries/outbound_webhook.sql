-- outbound_webhook.sql — P2-2 config CRUD + delivery matching.

-- name: CreateOutboundWebhook :one
INSERT INTO outbound_webhook (workspace_id, url, secret, event_types, active)
VALUES ($1, $2, $3, $4, COALESCE($5::bool, true))
RETURNING *;

-- name: ListOutboundWebhooks :many
SELECT * FROM outbound_webhook WHERE workspace_id = $1 ORDER BY created_at DESC;

-- name: DeleteOutboundWebhook :exec
DELETE FROM outbound_webhook WHERE id = $1;

-- name: ListWebhooksForEvent :many
-- P2-2 delivery matcher: active webhooks in the workspace whose event_types
-- array either is empty (subscribe-all) OR contains this event's type.
-- $2::text forces a scalar bind (sqlc otherwise types it as []string from
-- the column name, which breaks the = ANY(array) semantics).
SELECT * FROM outbound_webhook
WHERE workspace_id = $1
  AND active = true
  AND (cardinality(event_types) = 0 OR $2::text = ANY(event_types));
