-- knowledge_candidate: P2-5 knowledge sediment pool. Operators mark
-- candidates during runs/conversations; a batch-extract path promotes them
-- into Rules (P1-4) / Skills. Cairs the six-level maturity ladder (draft →
-- verified → proven, with stale/conflict decay states) — MVP carries the
-- column, transitions are manual via update API. No FK (repo rule).

CREATE TABLE knowledge_candidate (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- logical ref: workspace(id), app-enforced
    workspace_id UUID NOT NULL,
    -- where the candidate was surfaced: manual / run / conversation / agent
    source_type TEXT NOT NULL DEFAULT 'manual',
    source_id UUID,
    -- the candidate knowledge text (a rule statement, a tip, a gotcha).
    content TEXT NOT NULL,
    -- optional suggested capability/rule key to bind on extraction.
    suggested_key TEXT,
    -- workflow state: pending (in pool) / extracted (promoted to Rules) /
    -- rejected (discarded).
    status TEXT NOT NULL DEFAULT 'pending',
    -- cairn six-level maturity: draft / verified / proven / stale / conflict.
    -- MVP starts 'draft'; transitions are manual (P2-6 health-check automates
    -- stale later).
    maturity TEXT NOT NULL DEFAULT 'draft',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
