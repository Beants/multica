-- workflow_gate_script: workspace-scoped registry of gate scripts (P1-3
-- security contract #1 — source allowlist). Inline scripts (≤4KB) bypass
-- this table; gate nodes reference a registered script by name via
-- node.config.gate_script_ref.
--
-- Repo migration hard rules (AGENTS.md / CLAUDE.md, post-201 upstream):
--   * NO database foreign keys or cascading actions — every relationship
--     below is a plain UUID column annotated `-- logical ref:` and enforced
--     in the application layer.
--   * No table-level inline UNIQUE constraint (database-guidelines §9);
--     the (workspace_id, name) uniqueness is built as CREATE UNIQUE INDEX
--     CONCURRENTLY in migration 930.
--   * EVERY index, including indexes on this new table, is built with
--     CREATE INDEX CONCURRENTLY in its own single-statement migration file.
--     This file creates the table only.

CREATE TABLE workflow_gate_script (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- logical ref: workspace(id), enforced in app layer
    workspace_id UUID NOT NULL,
    name TEXT NOT NULL,
    -- 'python' alias deliberately omitted: harness 13 scripts all run under
    -- python3 (prd.md R3) and aliasing inflates the language CHECK surface
    -- for no caller benefit.
    language TEXT NOT NULL DEFAULT 'shell'
        CHECK (language IN ('shell', 'python3')),
    -- script_text: shell runs via `sh -c`, python3 runs via `python3 -c`.
    script_text TEXT NOT NULL,
    -- checksum: SHA-256 of script_text (hex digest); drift detection so an
    -- updated script_text forces a fresh checksum and audit trail.
    checksum TEXT NOT NULL,
    -- Per-script execution cap, <= node.config.gate_timeout_seconds.
    max_timeout_seconds INTEGER NOT NULL DEFAULT 60
        CHECK (max_timeout_seconds BETWEEN 1 AND 300),
    -- stdout+stderr truncation threshold (prd.md R6 contract #2).
    max_output_bytes INTEGER NOT NULL DEFAULT 1048576
        CHECK (max_output_bytes BETWEEN 1024 AND 10485760),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
