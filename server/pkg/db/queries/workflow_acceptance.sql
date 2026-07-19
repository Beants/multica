-- name: CreateAcceptance :one
-- One pending acceptance per step is enforced by the
-- idx_acceptance_pending_step partial unique index. step_instance_id binds
-- the acceptance node's step (spec_freeze vs final_acceptance).
INSERT INTO acceptance (run_id, step_instance_id, reviewer_id)
VALUES ($1, $2, sqlc.narg('reviewer_id'))
RETURNING *;

-- name: GetAcceptance :one
SELECT * FROM acceptance WHERE id = $1;

-- name: GetPendingAcceptanceByRun :one
-- The run's single undecided acceptance. Uniqueness is an engine
-- invariant, not a DB constraint: idx_acceptance_pending_step caps one
-- pending acceptance PER STEP, and the linear engine activates at most
-- one acceptance step at a time, so a run has at most one pending
-- acceptance. Kept as :one (the cleaner option) — callers never handle
-- multiple pending acceptances in P0.
SELECT * FROM acceptance
WHERE run_id = $1 AND status = 'pending';

-- name: ListAcceptancesForRun :many
-- Reject/rework cycles produce one row per round; decided rows are history.
SELECT * FROM acceptance
WHERE run_id = $1
ORDER BY created_at DESC;

-- name: DecideAcceptance :one
-- Guarded on status='pending' (blueprint §8.3): a concurrent double-decide
-- gets zero rows back and the loser re-reads the already-decided row.
-- reviewer_id is restamped with the DECIDING member (human decision path);
-- NULL keeps the activation-time reviewer (system auto_pass path).
UPDATE acceptance
SET status = sqlc.arg('new_status'),
    decided_at = now(),
    reviewer_id = COALESCE(sqlc.narg('reviewer_id'), reviewer_id),
    reject_reason = sqlc.narg('reject_reason'),
    reject_to_node_key = sqlc.narg('reject_to_node_key'),
    rework_context = sqlc.narg('rework_context'),
    updated_at = now()
WHERE id = sqlc.arg('id') AND status = 'pending'
RETURNING *;
