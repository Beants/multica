-- event_store: P2-1 global append-only event log. Every published event
-- (workflow verdict/dispatch/transition/sweeper/gate + business events) is
-- persisted here by a SubscribeAll listener; P2-3 metrics aggregate from it,
-- P2-2 webhooks read it, P2-4 dashboard queries it — none pull business
-- tables directly.
--
-- Repo migration hard rules (see 926): no FK (workspace_id/actor_id are
-- logical refs enforced in app layer); no in-table index — indexes land
-- CONCURRENTLY in their own single-statement migrations (937, 938).

CREATE TABLE event_store (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- logical ref: workspace(id), enforced in app layer. Empty for global
    -- system events (rare); the listener best-effort fills it from the Event.
    workspace_id UUID,
    event_type TEXT NOT NULL,
    actor_type TEXT,
    actor_id UUID,
    -- The full event Payload, JSONB for P2-3 aggregation queries (jsonb_path
    -- queries, ->> extractions). '{}' for events with no payload.
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Stable hash of (type + workspace + actor + payload): SubscribeAll
    -- retries/replays write the same key, ON CONFLICT DO NOTHING keeps the
    -- store idempotent. The unique index (938) backs this.
    dedup_key TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
