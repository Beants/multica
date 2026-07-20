-- name: CreateWorkflowGateScript :one
INSERT INTO workflow_gate_script (
    workspace_id, name, language, script_text, checksum,
    max_timeout_seconds, max_output_bytes
) VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: GetWorkflowGateScriptByName :one
-- Resolves node.config.gate_script_ref against the (workspace_id, name)
-- uniqueness built by migration 930. Inline scripts bypass this lookup.
SELECT * FROM workflow_gate_script
WHERE workspace_id = $1 AND name = $2;

-- name: ListWorkflowGateScripts :many
SELECT * FROM workflow_gate_script
WHERE workspace_id = $1
ORDER BY name;

-- name: UpdateWorkflowGateScript :one
-- workspace_id is part of the WHERE clause so a leaked id from another
-- workspace updates zero rows instead of cross-scoping the script.
UPDATE workflow_gate_script
SET name = $3,
    language = $4,
    script_text = $5,
    checksum = $6,
    max_timeout_seconds = $7,
    max_output_bytes = $8,
    updated_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: DeleteWorkflowGateScript :execrows
DELETE FROM workflow_gate_script
WHERE id = $1 AND workspace_id = $2;
