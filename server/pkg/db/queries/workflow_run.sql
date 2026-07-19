-- name: CreateWorkflowRun :one
-- source_id is the EXTERNAL work identifier (text, hook idempotency key);
-- intake_issue_id links the internal tracking issue (design.md §4.1).
INSERT INTO workflow_run (workspace_id, template_id, template_snapshot, source_type, source_id, intake_issue_id, context)
VALUES ($1, $2, $3, $4, sqlc.narg('source_id'), sqlc.narg('intake_issue_id'), $5)
RETURNING *;

-- name: GetWorkflowRun :one
SELECT * FROM workflow_run WHERE id = $1;

-- name: GetWorkflowRunInWorkspace :one
SELECT * FROM workflow_run WHERE id = $1 AND workspace_id = $2;

-- name: GetWorkflowRunForUpdate :one
-- Row lock for the engine's verdict/rework transactions (blueprint §8.1):
-- concurrent signals serialize on the run row before re-reading step state.
SELECT * FROM workflow_run WHERE id = $1 FOR UPDATE;

-- name: GetWorkflowRunBySource :one
-- Inbound-hook idempotency lookup (blueprint §8.3): a re-pushed delivery
-- finds the existing run and gets it back instead of creating a duplicate.
SELECT * FROM workflow_run
WHERE workspace_id = $1 AND source_type = $2 AND source_id = $3 AND template_id = $4;

-- name: ListWorkflowRuns :many
SELECT * FROM workflow_run
WHERE workspace_id = $1
ORDER BY started_at DESC;

-- name: UpdateWorkflowRunStatus :one
-- Guarded status transition (design.md §4.6): zero rows returned means the
-- run was not in the expected state — the engine abandons and re-reads.
-- completed_at is set only for terminal transitions.
UPDATE workflow_run
SET status = sqlc.arg('new_status'),
    completed_at = COALESCE(sqlc.narg('completed_at'), completed_at),
    updated_at = now()
WHERE id = sqlc.arg('id') AND status = sqlc.arg('expected_status')
RETURNING *;
