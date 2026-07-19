-- name: CreateStepTransition :one
-- Every guarded status change writes one row (design.md §4.6). ON CONFLICT
-- DO NOTHING against idx_step_transition_dedup makes a retried write a
-- no-op (returns no row) instead of duplicating history.
INSERT INTO step_transition (run_id, step_instance_id, from_status, to_status, attempt, trigger_by, payload)
VALUES ($1, $2, $3, $4, $5, $6, sqlc.narg('payload'))
ON CONFLICT DO NOTHING
RETURNING *;

-- name: ListStepTransitionsForRun :many
-- Run-detail timeline (AC4).
SELECT * FROM step_transition
WHERE run_id = $1
ORDER BY created_at ASC;

-- name: ListStepTransitionsForStep :many
SELECT * FROM step_transition
WHERE step_instance_id = $1
ORDER BY created_at ASC;

-- name: CountConsecutiveReworksForNode :one
-- Circuit breaker counter ① (design.md §4.4): reworks of this node since
-- its last pass — measures gate/failure loops. >= 3 pauses the run and
-- hands the intake issue to a human.
SELECT count(*) FROM step_transition st
JOIN step_instance si ON si.id = st.step_instance_id
WHERE si.run_id = $1
  AND si.node_key = $2
  AND st.to_status = 'rework'
  AND st.created_at > COALESCE(
      (
          SELECT max(st_pass.created_at)
          FROM step_transition st_pass
          JOIN step_instance si_pass ON si_pass.id = st_pass.step_instance_id
          WHERE si_pass.run_id = $1
            AND si_pass.node_key = $2
            AND st_pass.to_status = 'passed'
      ),
      '-infinity'::timestamptz
  );

-- name: CountRejectionsForRun :one
-- Circuit breaker counter ② (design.md §4.4): total acceptance rejections
-- in this run — measures acceptance-rejection loops, which counter ①
-- cannot see (the rejected node passes again before the next rejection,
-- resetting "since last pass"). >= 3 pauses the run + human handoff.
SELECT count(*) FROM acceptance
WHERE run_id = $1 AND status = 'rejected';
