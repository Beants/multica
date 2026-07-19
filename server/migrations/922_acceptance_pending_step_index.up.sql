-- One undecided acceptance per acceptance step (design.md §1). Linear flow
-- activates at most one acceptance step at a time, so this also guarantees
-- one pending acceptance per run; decided rows are kept as history across
-- reject/rework cycles. Keep this as the migration's only statement:
-- PostgreSQL rejects CREATE INDEX CONCURRENTLY inside a transaction or
-- multi-command string.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_acceptance_pending_step
    ON acceptance (step_instance_id)
    WHERE status = 'pending';
