-- gate_run: one row per gate-node execution (P1-3 MVP — script form only;
-- agent/rules/adversarial/hybrid forms land in P1-3b and reuse this table).
--
-- Repo migration hard rules (AGENTS.md / CLAUDE.md, post-201 upstream):
--   * NO database foreign keys or cascading actions — every relationship
--     below is a plain UUID column annotated `-- logical ref:` and enforced
--     in the application layer (gate.go txA/txB transactions).
--   * EVERY index, including indexes on this new table, is built with
--     CREATE INDEX CONCURRENTLY in its own single-statement migration file
--     (927, 928). This file creates the table only.
--
-- output JSONB shape (P1-3 design.md §txB):
--   {facts: [...], dispositions: [...], stdout: "...", stderr: "...",
--    truncated: bool, fix_hint: "..."}
-- status lifecycle: 'running' (txA INSERT) -> pass|block|warn|error (txB
-- UPDATE). The 'running' -> 'running' transition is forbidden by the
-- guarded UPDATE in queries/gate_run.sql (WHERE status='running').

CREATE TABLE gate_run (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- logical ref: step_instance(id), enforced in app layer
    step_instance_id UUID NOT NULL,
    -- logical ref: workflow_gate_script(id), enforced in app layer;
    -- NULL for inline scripts (gate_inline_script)
    script_id UUID,
    -- P1-3 MVP only writes 'script'; the CHECK carries the other four
    -- values so P1-3b activations can land without a follow-up migration.
    gate_type TEXT NOT NULL
        CHECK (gate_type IN ('script', 'agent', 'rules', 'adversarial', 'hybrid')),
    status TEXT NOT NULL
        CHECK (status IN ('pass', 'block', 'warn', 'error', 'running')),
    -- output JSONB shape: {facts, dispositions, stdout, stderr, truncated,
    -- fix_hint}; '{}' while status='running'.
    output JSONB NOT NULL DEFAULT '{}'::jsonb,
    duration_ms INTEGER NOT NULL DEFAULT 0,
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
