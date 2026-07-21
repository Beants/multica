-- outbound_delivery: P2-2 per-(webhook,event) delivery record. Auditable
-- proof of what was sent + the response. No FK (repo rule). No in-table
-- index (941 builds the webhook_id lookup CONCURRENTLY).

CREATE TABLE outbound_delivery (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- logical refs: outbound_webhook(id), event_store(id), app-enforced
    webhook_id UUID NOT NULL,
    event_id UUID NOT NULL,
    -- pending (queued) / delivered (2xx) / failed (non-2xx or network error)
    status TEXT NOT NULL DEFAULT 'pending',
    attempts INTEGER NOT NULL DEFAULT 0,
    response_code INTEGER,
    delivered_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
