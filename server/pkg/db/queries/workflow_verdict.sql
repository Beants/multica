-- name: CreateVerdict :one
-- UNIQUE(submission_id): one submission yields exactly one verdict. The
-- engine's system-derived verdict and an evaluator's agent verdict both
-- write through here.
INSERT INTO verdict (submission_id, step_instance_id, result, root_cause, confidence, evidence, verdict_by)
VALUES ($1, $2, $3, sqlc.narg('root_cause'), sqlc.narg('confidence'), sqlc.narg('evidence'), $4)
RETURNING *;

-- name: GetVerdict :one
SELECT * FROM verdict WHERE id = $1;

-- name: GetVerdictBySubmission :one
SELECT * FROM verdict WHERE submission_id = $1;

-- name: GetVerdictByStepInstance :one
SELECT * FROM verdict WHERE step_instance_id = $1;

-- name: ListVerdictsForRun :many
-- Run-detail trace view (AC4).
SELECT v.* FROM verdict v
JOIN step_instance si ON si.id = v.step_instance_id
WHERE si.run_id = $1
ORDER BY v.created_at ASC;
