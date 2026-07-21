-- outbound_delivery.sql — P2-2 delivery audit records.

-- name: InsertDelivery :one
INSERT INTO outbound_delivery (webhook_id, event_id, status, attempts, response_code, delivered_at)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: UpdateDeliveryStatus :one
-- Guarded: only a still-'pending' delivery accepts the terminal result, so
-- two concurrent delivery goroutines cannot double-finalize.
UPDATE outbound_delivery
SET status = $2,
    attempts = $3,
    response_code = $4,
    delivered_at = $5
WHERE id = $1 AND status = 'pending'
RETURNING *;
