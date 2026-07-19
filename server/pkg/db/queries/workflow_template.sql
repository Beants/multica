-- name: CreateWorkflowTemplate :one
INSERT INTO workflow_template (workspace_id, key, name, description, created_by)
VALUES ($1, $2, $3, $4, sqlc.narg('created_by'))
RETURNING *;

-- name: CreateTemplateVersion :one
-- Version lifecycle: fork the next draft of a (workspace, key) family.
-- version = MAX(version)+1 across the whole family — a src.version+1
-- computation collides on UNIQUE(workspace_id, key, version) when the same
-- published row is forked twice, MAX+1 cannot. Concurrent forks may race
-- on the same next version; the UNIQUE constraint turns the loser into an
-- error and the service retries. Nodes/edges are copied by the service
-- layer afterwards.
INSERT INTO workflow_template (workspace_id, key, name, description, version, status, created_by)
VALUES (
    $1,
    $2,
    $3,
    $4,
    (SELECT COALESCE(MAX(version), 0) + 1
     FROM workflow_template
     WHERE workspace_id = $1 AND key = $2),
    'draft',
    sqlc.narg('created_by')
)
RETURNING *;

-- name: ArchivePublishedWorkflowTemplateByKey :exec
-- Publishing vN+1 must first archive the currently-published row for the key
-- (idx_workflow_template_one_published enforces one published per key).
UPDATE workflow_template
SET status = 'archived', updated_at = now()
WHERE workspace_id = $1 AND key = $2 AND status = 'published';

-- name: GetWorkflowTemplate :one
SELECT * FROM workflow_template WHERE id = $1;

-- name: GetWorkflowTemplateInWorkspace :one
SELECT * FROM workflow_template WHERE id = $1 AND workspace_id = $2;

-- name: GetPublishedWorkflowTemplateByKey :one
-- Hook payload carries template_key; resolve to the newest published version.
SELECT * FROM workflow_template
WHERE workspace_id = $1 AND key = $2 AND status = 'published'
ORDER BY version DESC
LIMIT 1;

-- name: ListWorkflowTemplates :many
SELECT * FROM workflow_template
WHERE workspace_id = $1
ORDER BY created_at DESC;

-- name: UpdateWorkflowTemplate :one
-- Draft-only edit: published/archived templates are immutable (a new draft
-- version is created instead), so the guard lives in the query itself.
UPDATE workflow_template SET
    name = COALESCE(sqlc.narg('name'), name),
    description = COALESCE(sqlc.narg('description'), description),
    updated_at = now()
WHERE id = $1 AND status = 'draft'
RETURNING *;

-- name: UpdateWorkflowTemplateStatus :one
-- Guarded lifecycle transition (draft -> published -> archived). Zero rows
-- returned means the template was not in the expected state; the service
-- re-reads and reports the conflict.
UPDATE workflow_template
SET status = sqlc.arg('new_status'),
    updated_at = now()
WHERE id = sqlc.arg('id') AND status = sqlc.arg('expected_status')
RETURNING *;
