-- workflow_rule.sql — P1-4 Rules asset CRUD + soft-injection lookup.

-- name: CreateWorkflowRule :one
-- NULLIF on status so an empty-string default (common from API clients that
-- serialize omitempty strings as "") still lands at 'active' — a bare
-- COALESCE would pass the empty string through and trip the status CHECK.
INSERT INTO workflow_rule (workspace_id, name, level, scope, content, config, status)
VALUES ($1, $2, $3, $4, $5, $6, COALESCE(NULLIF($7::text, ''), 'active'))
RETURNING *;

-- name: GetWorkflowRule :one
SELECT * FROM workflow_rule WHERE id = $1;

-- name: ListWorkflowRules :many
SELECT * FROM workflow_rule WHERE workspace_id = $1 ORDER BY created_at DESC;

-- name: DeleteWorkflowRule :exec
DELETE FROM workflow_rule WHERE id = $1;

-- name: ListSoftRulesForAgent :many
-- P1-4 soft injection: the soft, context_inject rules bound (target_type=
-- agent) to the dispatching agent — their content is woven into the handoff
-- note so the agent sees team constraints at dispatch time. status='active'
-- only; archived/draft rules are invisible to dispatch.
SELECT r.* FROM workflow_rule r
JOIN workflow_rule_binding b ON b.rule_id = r.id
WHERE r.workspace_id = $1
  AND r.level = 'soft'
  AND r.status = 'active'
  AND b.enforcement = 'context_inject'
  AND b.target_type = 'agent'
  AND b.target_id = $2
ORDER BY r.created_at;
