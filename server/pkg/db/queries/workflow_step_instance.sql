-- name: CreateStepInstance :one
-- Activation writes status='active'; pre-creation of the next node writes
-- status='pending' (only one node ahead, inventory D-3). The
-- uq_step_instance_attempt constraint makes a duplicate activation a
-- unique-violation instead of a double dispatch.
INSERT INTO step_instance (run_id, node_key, status, parent_step_id, attempt, deadline_at)
VALUES ($1, $2, $3, sqlc.narg('parent_step_id'), $4, sqlc.narg('deadline_at'))
RETURNING *;

-- name: GetStepInstance :one
SELECT * FROM step_instance WHERE id = $1;

-- name: GetStepInstanceForUpdate :one
-- Verdict consumption re-reads the step inside the transaction (blueprint
-- §8.1): an already-terminal step makes the engine ignore the duplicate
-- signal.
SELECT * FROM step_instance WHERE id = $1 FOR UPDATE;

-- name: GetStepInstanceByTask :one
-- task event mapping + mat_ token resolution: the daemon/CLI only knows
-- the agent_task id.
SELECT * FROM step_instance WHERE agent_task_id = $1;

-- name: GetLatestStepInstanceForNode :one
SELECT * FROM step_instance
WHERE run_id = $1 AND node_key = $2
ORDER BY attempt DESC
LIMIT 1;

-- name: GetStepInstanceForNodeWithStatus :one
-- Pre-creation check ("is there already a pending row for this node?") and
-- active-step lookup share this shape.
SELECT * FROM step_instance
WHERE run_id = $1 AND node_key = $2 AND status = $3
ORDER BY attempt DESC
LIMIT 1;

-- name: ListStepInstancesForRun :many
SELECT * FROM step_instance
WHERE run_id = $1
ORDER BY created_at ASC;

-- name: ListActiveStepInstancesForRun :many
-- Rework conflict check (blueprint §8.1): rejecting into a node that still
-- has an in-flight step must be refused or merged by the engine.
SELECT * FROM step_instance
WHERE run_id = $1 AND status IN ('active', 'dispatched', 'running')
ORDER BY created_at ASC;

-- name: UpdateStepInstanceStatus :one
-- Guarded status transition (design.md §4.6): zero rows returned means the
-- step was not in the expected state — the engine abandons and re-reads.
-- started_at/finished_at are pinned by the caller on the transitions that
-- need them.
UPDATE step_instance
SET status = sqlc.arg('new_status'),
    started_at = COALESCE(sqlc.narg('started_at'), started_at),
    finished_at = COALESCE(sqlc.narg('finished_at'), finished_at),
    updated_at = now()
WHERE id = sqlc.arg('id') AND status = sqlc.arg('expected_status')
RETURNING *;

-- name: UpdateStepInstanceDispatch :one
-- Links the dispatch artifacts after EnqueueTaskForIssueWithHandoff
-- (design.md §4.1 activation sequence).
UPDATE step_instance
SET agent_id = $2,
    agent_task_id = $3,
    issue_id = $4,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: UpdateStepInstanceExitFields :one
-- On pass the engine copies the submission's exit_fields onto the step so
-- downstream context assembly reads one place.
UPDATE step_instance
SET exit_fields = $2,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: ListStepInstancesNeedingSweep :many
-- P1-5 sweeper candidate set (workflow/seed.go §P1-5): step_instance joined
-- to a still-running workflow_run, in one of the in-flight statuses, AND
-- matching at least one self-heal condition. The sweeper classifies each row
-- by which condition matched (no agent_task_id / deadline expired / blocked
-- too long) and routes it through the engine helpers — never via raw
-- agent_task_queue writes (see sweeper.go boundary comment).
SELECT si.* FROM step_instance si
JOIN workflow_run wr ON si.run_id = wr.id
WHERE wr.status = 'running'
  AND si.status IN ('active', 'running', 'blocked')
  AND (
        si.agent_task_id IS NULL
        OR (si.status = 'running' AND si.deadline_at IS NOT NULL AND si.deadline_at < now())
        OR (si.status = 'blocked' AND si.started_at IS NOT NULL AND si.started_at < now() - ($1::int || ' seconds')::interval)
      )
ORDER BY si.started_at ASC NULLS LAST, si.created_at ASC;
