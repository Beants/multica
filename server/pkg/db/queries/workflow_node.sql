-- name: CreateWorkflowNode :one
INSERT INTO workflow_node (template_id, node_key, type, name, config, position)
VALUES ($1, $2, $3, $4, $5, sqlc.narg('position'))
RETURNING *;

-- name: GetWorkflowNode :one
SELECT * FROM workflow_node WHERE id = $1;

-- name: GetWorkflowNodeByKey :one
SELECT * FROM workflow_node WHERE template_id = $1 AND node_key = $2;

-- name: ListWorkflowNodes :many
SELECT * FROM workflow_node
WHERE template_id = $1
ORDER BY created_at ASC;

-- name: UpdateWorkflowNode :one
UPDATE workflow_node SET
    name = COALESCE(sqlc.narg('name'), name),
    config = COALESCE(sqlc.narg('config'), config),
    position = COALESCE(sqlc.narg('position'), position),
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: DeleteWorkflowNode :exec
DELETE FROM workflow_node WHERE id = $1;

-- name: DeleteWorkflowNodesForTemplate :exec
-- Draft edits rewrite the node set wholesale; edges cascade via FK.
DELETE FROM workflow_node WHERE template_id = $1;
