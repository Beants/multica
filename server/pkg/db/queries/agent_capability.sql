-- agent_capability.sql — P1-7 capability routing queries.

-- name: UpsertAgentCapability :one
-- Management API: declare or refresh an agent's proficiency for a capability
-- key. ON CONFLICT on (agent_id, capability_key) keeps one row per pair (the
-- unique index 932 backs this).
INSERT INTO agent_capability (agent_id, capability_key, proficiency, evidence, updated_at)
VALUES ($1, $2, $3, $4, now())
ON CONFLICT (agent_id, capability_key) DO UPDATE
SET proficiency = EXCLUDED.proficiency,
    evidence = EXCLUDED.evidence,
    updated_at = now()
RETURNING *;

-- name: ListAgentCapabilities :many
SELECT * FROM agent_capability WHERE agent_id = $1 ORDER BY capability_key;

-- name: DeleteAgentCapability :exec
DELETE FROM agent_capability WHERE id = $1;

-- name: MatchAgentByCapability :one
-- P1-7 dispatch matcher: among the workspace's agents, pick the one that
-- declared ALL required capability keys (proficiency > 0) with the highest
-- total proficiency. Returns pgx.ErrNoRows when no agent qualifies — the
-- caller (activateAgentNode) falls back to the workspace default agent.
-- Workspace scoping is app-enforced via the agent join (no FK per repo rule).
SELECT ac.agent_id
FROM agent_capability ac
JOIN agent a ON a.id = ac.agent_id
WHERE a.workspace_id = $1
  AND ac.proficiency > 0
  AND ac.capability_key = ANY($2::text[])
GROUP BY ac.agent_id
HAVING COUNT(DISTINCT ac.capability_key) = cardinality($2::text[])
ORDER BY SUM(ac.proficiency) DESC
LIMIT 1;
