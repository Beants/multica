-- outbound_webhook: P2-2 external subscription endpoint. A workspace
-- registers URLs that multica POSTs event payloads to (with HMAC-SHA256
-- signature). Distinct from the inbound webhook_delivery table (093, which
-- records GitHub/GitLab → multica deliveries). No FK (repo rule).

CREATE TABLE outbound_webhook (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- logical ref: workspace(id), app-enforced
    workspace_id UUID NOT NULL,
    url TEXT NOT NULL,
    -- HMAC-SHA256 signing secret; the POST carries X-Multica-Signature =
    -- hex(hmac(secret, body)). Generated server-side on create.
    secret TEXT NOT NULL,
    -- Empty array = all events; otherwise the webhook only receives event_type
    -- values present in this list.
    event_types TEXT[] NOT NULL DEFAULT '{}',
    active BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
