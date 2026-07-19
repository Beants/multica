-- Workflow engine core schema (P0, in-repo fork — 900+ migration range per
-- blueprint §6 fork hygiene; 175-899 stays reserved for upstream).
--
-- Ten tables: workflow_template / workflow_node / workflow_edge /
-- workflow_hook / workflow_run / step_instance / submission / verdict /
-- acceptance / step_transition. Linear progression only in P0 —
-- workflow_edge.condition stays NULL (JSONB column reserved, P1 evaluates),
-- fan_out/converge node types exist in the CHECK for P1 forward-compat but
-- are never instantiated by P0 seeds.
--
-- Repo migration hard rules (AGENTS.md / CLAUDE.md, post-201 upstream):
--   * NO database foreign keys or cascading actions — every relationship
--     below is a plain UUID column annotated `-- logical ref:` and enforced
--     in the application layer (engine transactions; see the header comment
--     of internal/workflow/engine.go).
--   * EVERY index, including unique indexes on these new tables, is built
--     with CREATE [UNIQUE] INDEX CONCURRENTLY in its own single-statement
--     migration file (902-925). This file creates tables only.
--
-- Concurrency hard guarantees (blueprint §8.2, design.md §1) — enforced by
-- the unique indexes in migrations 902-925:
--   * step_instance: uq_step_instance_attempt UNIQUE NULLS NOT DISTINCT
--     (run_id, node_key, parent_step_id, attempt) — parent_step_id is always
--     NULL in P0 and PG treats NULLs as distinct under a plain UNIQUE,
--     which would silently allow duplicate attempts. NULLS NOT DISTINCT
--     (PG 15+) collapses NULL into a single value so the guard actually
--     fires (precedent: 084_task_usage_dashboard_rollup).
--   * workflow_run: uq_workflow_run_source UNIQUE (workspace_id,
--     source_type, source_id, template_id) — inbound-hook idempotency.
--     source_id stays NULL for manual runs; a plain UNIQUE keeps NULLs
--     distinct, so manual runs never dedupe (desired) while
--     hook/issue-sourced runs do.
--   * submission: uq_submission_step UNIQUE (step_instance_id) —
--     step_instance is already per-attempt, so an attempt column in the key
--     would be redundant (design.md §7 deviation #7).
--   * verdict: uq_verdict_submission UNIQUE (submission_id) — one
--     submission yields one verdict.
--   * acceptance: idx_acceptance_pending_step partial unique on
--     (step_instance_id) WHERE status='pending' so each acceptance step has
--     at most one undecided acceptance. Linear flow activates at most one
--     acceptance step at a time, so a run still has at most one pending
--     acceptance at any moment; decided rows are kept as history across
--     reject/rework cycles.
--   * step_transition: idx_step_transition_dedup unique on
--     (step_instance_id, from_status, to_status, attempt) so retried
--     transition writes don't duplicate history (circuit-breaker counting
--     reads this table).

CREATE TABLE workflow_template (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- logical ref: workspace(id), enforced in app layer
    workspace_id UUID NOT NULL,
    key TEXT NOT NULL,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    version INTEGER NOT NULL DEFAULT 1,
    status TEXT NOT NULL DEFAULT 'draft'
        CHECK (status IN ('draft', 'published', 'archived')),
    -- Nullable: seed templates are system-created. No FK, mirroring
    -- squad.creator_id — members must not block template lifecycle.
    -- logical ref: "user"(id), enforced in app layer
    created_by UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE workflow_node (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- logical ref: workflow_template(id), enforced in app layer
    template_id UUID NOT NULL,
    node_key TEXT NOT NULL,
    type TEXT NOT NULL
        CHECK (type IN ('agent', 'gate', 'fan_out', 'converge', 'acceptance', 'end')),
    name TEXT NOT NULL,
    -- config carries agent_selector / role (executor|evaluator|reviewer) /
    -- instructions / exit_fields schema / timeout / fail policy / auto_pass.
    config JSONB NOT NULL DEFAULT '{}'::jsonb,
    -- Canvas coordinates; unused until the P3 node editor.
    position JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE workflow_edge (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- logical ref: workflow_template(id), enforced in app layer
    template_id UUID NOT NULL,
    -- logical ref: workflow_node(id), enforced in app layer
    from_node_id UUID NOT NULL,
    -- logical ref: workflow_node(id), enforced in app layer
    to_node_id UUID NOT NULL,
    -- P0: always NULL (linear progression, engine takes the default edge).
    -- JSONB reserved for the P1 verdict/exit_fields expression evaluator.
    condition JSONB,
    priority INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Inbound-hook credentials. token_hash stores SHA-256(token) — never the
-- cleartext (deliberately better than autopilot_trigger.webhook_token's
-- plaintext, see design.md §1). Delivery auditing is downgraded for P0 to
-- last_used_at + structured logs; a real delivery table arrives with the
-- P2 outbound-webhook work (design.md §7 deviation #11).
CREATE TABLE workflow_hook (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- logical ref: workspace(id), enforced in app layer
    workspace_id UUID NOT NULL,
    -- logical ref: workflow_template(id), enforced in app layer
    template_id UUID NOT NULL,
    token_hash TEXT NOT NULL,
    name TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'disabled')),
    last_used_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE workflow_run (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- logical ref: workspace(id), enforced in app layer
    workspace_id UUID NOT NULL,
    -- App-layer rule (previously ON DELETE RESTRICT): templates with runs
    -- must be archived, not deleted, so run history and template_snapshot
    -- stay auditable.
    -- logical ref: workflow_template(id), enforced in app layer
    template_id UUID NOT NULL,
    -- Frozen at publish/start; the run never re-reads the live template.
    template_snapshot JSONB NOT NULL,
    status TEXT NOT NULL DEFAULT 'running'
        CHECK (status IN ('running', 'paused', 'completed', 'failed', 'cancelled', 'waiting_acceptance')),
    source_type TEXT NOT NULL
        CHECK (source_type IN ('issue', 'hook', 'autopilot', 'manual')),
    -- External work identifier (hook payload's source_id, e.g. the requirement
    -- ID in the upstream system). TEXT on purpose: external IDs are not
    -- necessarily UUIDs. This is the hook idempotency key — re-pushes of the
    -- same external work hit uq_workflow_run_source and return the existing
    -- run. NULL for manual runs (NULLs stay distinct, manual never dedupes).
    source_id TEXT,
    -- The tracking issue created at run intake (design.md §4.1). Separate
    -- from source_id: source_id is the EXTERNAL identifier used for
    -- idempotency; intake_issue_id is the INTERNAL parent issue.
    -- logical ref: issue(id), enforced in app layer (set-null on issue
    -- delete is app-handled, previously ON DELETE SET NULL)
    intake_issue_id UUID,
    context JSONB NOT NULL DEFAULT '{}'::jsonb,
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE step_instance (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- logical ref: workflow_run(id), enforced in app layer
    run_id UUID NOT NULL,
    -- References template_snapshot.nodes[].node_key, not workflow_node —
    -- the snapshot is frozen JSONB, so no reference is possible.
    node_key TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'active', 'dispatched', 'running', 'passed', 'failed', 'blocked', 'rework', 'skipped')),
    -- logical ref: agent(id), enforced in app layer (set-null on agent
    -- delete is app-handled, previously ON DELETE SET NULL)
    agent_id UUID,
    -- logical ref: agent_task_queue(id), enforced in app layer (set-null on
    -- task delete is app-handled, previously ON DELETE SET NULL)
    agent_task_id UUID,
    -- logical ref: issue(id), enforced in app layer (set-null on issue
    -- delete is app-handled, previously ON DELETE SET NULL)
    issue_id UUID,
    -- Fan-out child steps only; always NULL in P0 (see NULLS NOT DISTINCT
    -- rationale in the header comment).
    -- logical ref: step_instance(id), enforced in app layer
    parent_step_id UUID,
    attempt INTEGER NOT NULL DEFAULT 1,
    exit_fields JSONB,
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    deadline_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Agent work product for one step attempt. status uses the harness
-- four-state vocabulary (uppercase, mechanism inventory D-2); gaps carries
-- the known-gaps list for DONE_WITH_CONCERNS/BLOCKED; artifacts holds only
-- durable references (PR URL / branch / attachment IDs — never workdir
-- relative paths, D-11).
CREATE TABLE submission (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- logical ref: step_instance(id), enforced in app layer
    step_instance_id UUID NOT NULL,
    -- logical ref: agent_task_queue(id), enforced in app layer (set-null on
    -- task delete is app-handled, previously ON DELETE SET NULL)
    task_id UUID,
    status TEXT NOT NULL
        CHECK (status IN ('DONE', 'DONE_WITH_CONCERNS', 'BLOCKED', 'NEEDS_CONTEXT')),
    gaps JSONB,
    artifacts JSONB,
    exit_fields JSONB,
    raw_summary TEXT,
    -- daemon/CLI retry safety (blueprint §8.2): callers pass a stable key
    -- and re-posts collide instead of duplicating.
    idempotency_key TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Naming discipline (inventory 1.14): result only ever holds the flow
-- verdict pass/fail/blocked. Business review opinions are called `decision`
-- and live on acceptance/review surfaces, never here.
CREATE TABLE verdict (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- logical ref: submission(id), enforced in app layer
    submission_id UUID NOT NULL,
    -- Denormalized from submission for run-detail queries; always equals
    -- submission.step_instance_id (written in the same transaction).
    -- logical ref: step_instance(id), enforced in app layer
    step_instance_id UUID NOT NULL,
    result TEXT NOT NULL
        CHECK (result IN ('pass', 'fail', 'blocked')),
    root_cause TEXT,
    confidence NUMERIC,
    evidence JSONB,
    verdict_by TEXT NOT NULL
        CHECK (verdict_by IN ('system', 'agent', 'human')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE acceptance (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- logical ref: workflow_run(id), enforced in app layer
    run_id UUID NOT NULL,
    -- The acceptance node's step this decision belongs to (distinguishes
    -- mid-flow Spec Freeze from final acceptance via the step's node_key).
    -- logical ref: step_instance(id), enforced in app layer
    step_instance_id UUID NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'approved', 'rejected')),
    -- logical ref: member(id), enforced in app layer (set-null on member
    -- delete is app-handled, previously ON DELETE SET NULL)
    reviewer_id UUID,
    decided_at TIMESTAMPTZ,
    reject_reason TEXT,
    reject_to_node_key TEXT,
    rework_context JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE step_transition (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- logical ref: workflow_run(id), enforced in app layer
    run_id UUID NOT NULL,
    -- logical ref: step_instance(id), enforced in app layer
    step_instance_id UUID NOT NULL,
    -- No CHECK on from/to: initial activation writes a sentinel from_status
    -- and the enum would only duplicate step_instance's.
    from_status TEXT NOT NULL,
    to_status TEXT NOT NULL,
    attempt INTEGER NOT NULL,
    trigger_by TEXT NOT NULL
        CHECK (trigger_by IN ('verdict', 'sweeper', 'human', 'system', 'engine')),
    payload JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
