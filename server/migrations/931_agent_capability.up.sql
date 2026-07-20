-- agent_capability: P1-7 capability routing. capability_key + proficiency
-- per agent; the dispatch matcher (activate.go) selects the most proficient
-- agent for a node's required_capabilities. evidence JSONB holds success-
-- rate / sample-size provenance (P2 auto-writeback from event_store; P1 is
-- hand-annotated via API).
--
-- Repo migration hard rules (AGENTS.md / CLAUDE.md, see 926 for the long
-- form): no database FK (agent_id is a logical ref enforced in app layer);
-- no in-table index here — the unique (agent_id, capability_key) index is
-- built CONCURRENTLY in its own single-statement migration (932) so the
-- ON CONFLICT upsert in queries/agent_capability.sql dedupes declarations.

CREATE TABLE agent_capability (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- logical ref: agent(id), enforced in app layer
    agent_id UUID NOT NULL,
    capability_key TEXT NOT NULL,
    -- 0-100; 0 = declared but no demonstrated proficiency. The matcher
    -- filters proficiency > 0 so a bare declaration never wins over evidence.
    proficiency SMALLINT NOT NULL DEFAULT 0 CHECK (proficiency BETWEEN 0 AND 100),
    evidence JSONB,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
