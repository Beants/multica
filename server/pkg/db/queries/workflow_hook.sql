-- name: CreateWorkflowHook :one
-- token_hash is SHA-256(token); the cleartext is returned to the creator
-- once and never stored.
INSERT INTO workflow_hook (workspace_id, template_id, token_hash, name)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetWorkflowHook :one
SELECT * FROM workflow_hook WHERE id = $1;

-- name: GetWorkflowHookInWorkspace :one
SELECT * FROM workflow_hook WHERE id = $1 AND workspace_id = $2;

-- name: GetWorkflowHookByTokenHash :one
-- Inbound-hook auth lookup. The handler still checks status='active' so a
-- disabled hook 401s rather than silently dispatching.
SELECT * FROM workflow_hook WHERE token_hash = $1;

-- name: ListWorkflowHooks :many
SELECT * FROM workflow_hook
WHERE workspace_id = $1
ORDER BY created_at DESC;

-- name: SetWorkflowHookStatus :one
UPDATE workflow_hook
SET status = $2
WHERE id = $1 AND workspace_id = $3
RETURNING *;

-- name: TouchWorkflowHookLastUsedAt :exec
-- P0 delivery audit (design.md §1): bumped after every inbound POST,
-- regardless of dispatch outcome; the structured log carries the detail.
UPDATE workflow_hook
SET last_used_at = now()
WHERE id = $1;
