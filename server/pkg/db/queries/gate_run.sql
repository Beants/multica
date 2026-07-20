-- name: CreateGateRun :one
-- txA of the gate script double-transaction boundary (design.md §txA):
-- INSERT a 'running' row, COMMIT, then run the script outside any tx so the
-- run-row lock is only held for milliseconds in txB.
INSERT INTO gate_run (step_instance_id, script_id, gate_type, status, started_at)
VALUES ($1, $2, $3, 'running', now())
RETURNING *;

-- name: GetGateRun :one
SELECT * FROM gate_run WHERE id = $1;

-- name: UpdateGateRunResult :one
-- Guarded transition (database-guidelines Query Patterns §并发控制标准模式):
-- only a still-'running' row accepts the result. A zero-row return means the
-- run was already finalized (server crash mid-script, retry, or duplicate
-- signal) and the engine must abandon + re-read.
UPDATE gate_run
SET status = $2,
    output = $3,
    duration_ms = $4,
    finished_at = now()
WHERE id = $1 AND status = 'running'
RETURNING *;

-- name: ListGateRunsByStep :many
SELECT * FROM gate_run
WHERE step_instance_id = $1
ORDER BY created_at DESC;

-- name: ListGateRunsForRun :many
-- P1-6 diagnosis: every gate_run row for a run's steps (batch load). The
-- output JSONB carries {stdout, stderr, facts, dispositions} and status
-- (pass/block/warn/error) — the stderr/stdout + gate-reject elements of
-- the seven-element diagnosis.
SELECT * FROM gate_run
WHERE step_instance_id IN (SELECT id FROM step_instance WHERE run_id = $1)
ORDER BY created_at DESC;

-- name: GetRunningGateRunByStep :one
-- P1-3b: lookup of the still-running gate_run bound to a step (agent /
-- adversarial gate form). Returns pgx.ErrNoRows when no gate_run exists
-- for the step or it has already been finalized — the common non-gate
-- step case is a zero-row lookup, kept cheap.
SELECT * FROM gate_run
WHERE step_instance_id = $1 AND status = 'running'
ORDER BY created_at DESC
LIMIT 1;
