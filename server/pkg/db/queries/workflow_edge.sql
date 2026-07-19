-- name: CreateWorkflowEdge :one
INSERT INTO workflow_edge (template_id, from_node_id, to_node_id, condition, priority)
VALUES ($1, $2, $3, sqlc.narg('condition'), $4)
RETURNING *;

-- name: GetWorkflowEdge :one
SELECT * FROM workflow_edge WHERE id = $1;

-- name: ListWorkflowEdgesForTemplate :many
SELECT * FROM workflow_edge
WHERE template_id = $1
ORDER BY created_at ASC;

-- name: ListWorkflowEdgesFromNode :many
-- EvaluateEdges source: P0 takes the single condition IS NULL default edge.
-- Ordering matches Snapshot.NextAfter: the LOWEST priority value wins the
-- tiebreak, creation order settles exact ties (P1 conditional routing).
SELECT * FROM workflow_edge
WHERE from_node_id = $1
ORDER BY priority ASC, created_at ASC;

-- name: DeleteWorkflowEdge :exec
DELETE FROM workflow_edge WHERE id = $1;

-- name: DeleteWorkflowEdgesForTemplate :exec
DELETE FROM workflow_edge WHERE template_id = $1;
