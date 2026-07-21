-- P2-5: list a workspace's candidate pool (always workspace-scoped).
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_knowledge_candidate_workspace
    ON knowledge_candidate (workspace_id);
