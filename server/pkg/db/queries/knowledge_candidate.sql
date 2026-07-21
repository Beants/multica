-- knowledge_candidate.sql — P2-5 knowledge sediment pool CRUD.

-- name: CreateKnowledgeCandidate :one
-- NULLIF on status/maturity so an empty default lands at pending/draft
-- (same rationale as CreateWorkflowRule).
INSERT INTO knowledge_candidate (workspace_id, source_type, source_id, content, suggested_key, status, maturity)
VALUES ($1, $2, $3, $4, $5, COALESCE(NULLIF($6::text, ''), 'pending'), COALESCE(NULLIF($7::text, ''), 'draft'))
RETURNING *;

-- name: ListKnowledgeCandidates :many
-- P2-5 pool feed. status filter: '' = all (pending + extracted + rejected).
SELECT * FROM knowledge_candidate
WHERE workspace_id = $1
  AND ($2::text = '' OR status = $2)
ORDER BY created_at DESC;

-- name: UpdateKnowledgeCandidateStatus :one
-- Promote/discard: extracted (promoted to Rules) / rejected. Guarded by
-- expected status so two concurrent extracts don't double-promote.
UPDATE knowledge_candidate
SET status = $2, updated_at = now()
WHERE id = $1 AND status = $3
RETURNING *;

-- name: DeleteKnowledgeCandidate :exec
DELETE FROM knowledge_candidate WHERE id = $1;

-- name: ListStaleCandidates :many
-- P2-6 health: pending candidates older than the age threshold need review
-- (time factor; code-change correlation is the second factor, follow-up).
SELECT * FROM knowledge_candidate
WHERE workspace_id = $1
  AND status = 'pending'
  AND updated_at < now() - ($2::int || ' seconds')::interval
ORDER BY updated_at ASC;

-- name: UpdateCandidateMaturity :one
-- P2-6: mark a candidate's maturity (draft/verified/proven/stale/conflict)
-- after review. Manual via API for MVP; an automated stale sweeper is a
-- follow-up (mirrors the workflow sweeper pattern).
UPDATE knowledge_candidate
SET maturity = $2, updated_at = now()
WHERE id = $1
RETURNING *;
