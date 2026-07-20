-- workflow_rule: P1-4 Rules asset (design.md §2 支柱 5). A rule is a
-- team constraint at one of three levels (hard/soft/safety) that binds to a
-- node/template/agent/project via workflow_rule_binding (934). P1-4 MVP
-- delivers the data layer + CRUD API + soft/context_inject handoff注入;
-- hard gate-check execution lands in P1-4b (gate_type=rules engine).
--
-- Repo migration hard rules (see 926): no FK (workspace_id is a logical ref
-- enforced in app layer); no in-table index — the workspace_id lookup index
-- is built CONCURRENTLY in its own single-statement migration (935).

CREATE TABLE workflow_rule (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- logical ref: workspace(id), enforced in app layer
    workspace_id UUID NOT NULL,
    name TEXT NOT NULL,
    -- hard = machine-checkable red line (gate_check, P1-4b);
    -- soft = advisory, injected into agent context (P1-4);
    -- safety = high-risk pre-confirm (P1-4b).
    level TEXT NOT NULL CHECK (level IN ('hard', 'soft', 'safety')),
    -- binding granularity the rule is intended for.
    scope TEXT NOT NULL DEFAULT 'workspace' CHECK (scope IN ('workspace', 'project', 'agent')),
    content TEXT NOT NULL,
    -- globs / alwaysApply / matcher config (design.md). '{}' for plain text.
    config JSONB NOT NULL DEFAULT '{}'::jsonb,
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'draft', 'archived')),
    version INTEGER NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
