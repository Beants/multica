-- Task-completion events resolve their step by agent_task_id. Partial
-- because steps carry no task until dispatched. Keep this as the
-- migration's only statement: PostgreSQL rejects CREATE INDEX CONCURRENTLY
-- inside a transaction or multi-command string.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_step_instance_task
    ON step_instance (agent_task_id)
    WHERE agent_task_id IS NOT NULL;
