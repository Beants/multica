-- workflow_rule_binding.sql — P1-4 rule→target bindings.

-- name: CreateWorkflowRuleBinding :one
-- NULLIF on enforcement (same rationale as CreateWorkflowRule's status).
INSERT INTO workflow_rule_binding (rule_id, target_type, target_id, enforcement)
VALUES ($1, $2, $3, COALESCE(NULLIF($4::text, ''), 'context_inject'))
RETURNING *;

-- name: ListWorkflowRuleBindings :many
SELECT * FROM workflow_rule_binding WHERE rule_id = $1 ORDER BY created_at;

-- name: DeleteWorkflowRuleBinding :exec
DELETE FROM workflow_rule_binding WHERE id = $1;
