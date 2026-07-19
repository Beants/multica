-- name: CreateSubmission :one
INSERT INTO submission (step_instance_id, task_id, status, gaps, artifacts, exit_fields, raw_summary, idempotency_key)
VALUES ($1, sqlc.narg('task_id'), $2, sqlc.narg('gaps'), sqlc.narg('artifacts'), sqlc.narg('exit_fields'), sqlc.narg('raw_summary'), sqlc.narg('idempotency_key'))
RETURNING *;

-- name: GetSubmission :one
SELECT * FROM submission WHERE id = $1;

-- name: GetSubmissionByStepInstance :one
-- UNIQUE(step_instance_id) guarantees at most one row.
SELECT * FROM submission WHERE step_instance_id = $1;

-- name: GetSubmissionByIdempotencyKey :one
-- daemon/CLI retry path: a re-post with a known key returns the original
-- row instead of hitting the unique index. Scoped per step — the same
-- caller key is valid on other steps (idx is (step_instance_id, key)).
SELECT * FROM submission WHERE step_instance_id = $1 AND idempotency_key = $2;

-- name: ListSubmissionsForRun :many
-- Run-detail trace view (AC4).
SELECT s.* FROM submission s
JOIN step_instance si ON si.id = s.step_instance_id
WHERE si.run_id = $1
ORDER BY s.created_at ASC;
