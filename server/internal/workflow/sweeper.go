package workflow

// sweeper.go — P1-5 workflow-level periodic self-healer. Independent from
// runtime_sweeper.go (cmd/server/runtime_sweeper.go): that goroutine owns
// agent_task_queue lifecycle (stale runtimes, dispatched/running timeouts,
// queued backlog drain); this one owns workflow semantics (re-dispatch
// taskless active steps, reset deadline-expired running steps, pause runs
// with long-blocked steps).
//
// Boundary rule (quality-guidelines.md "裸 sqlc 改业务实体状态" forbids
// bypassing the service layer): Sweeper NEVER issues a raw
// `UPDATE agent_task_queue` / `DELETE FROM agent_task_queue` / etc. Every
// task-level effect goes through service.TaskService via Engine.activateNode
// (which calls EnqueueTaskForIssueWithHandoff) — the single dispatch path.
// The mechanical guarantee is the boundary self-check
// TestSweeper_DoesNotTouchAgentTaskQueueDirectly (grep
// `agent_task_queue`-write patterns in this file).
//
// Three self-heal cases (PRD R3):
//  ① active + no agent_task_id  → re-dispatch via Engine.activateNode
//  ② running + deadline_at<now  → reset to pending + re-activate + dispatch
//  ③ blocked > BlockedTimeout   → pause run + notify initiator
//
// Case ④ (consecutive reworks ≥ 3 → human handoff, circuitBreakerLimit in
// seed.go) is enforced synchronously inside Engine.RequestRework on the
// rejection path; the breaker fires BEFORE any candidate row could reach
// this sweeper's SELECT. A reconcile pass for the crash window between
// rejection and breaker evaluation is tracked in P2.

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Default sweep cadence + blocked-timeout. Tuned for P1:
//   - 60s tick catches stuck steps within a minute of the condition
//     becoming true, cheaply enough that the SELECT is negligible against
//     the runtime_sweeper's 30s tick on the same DB.
//   - 30min blocked-timeout is well past any legitimate "paused on
//     blocked verdict while a human is paging" window (the initiator is
//     inboxed the moment the run pauses), but short enough that a
//     genuinely wedged run is reconciled within an hour.
const (
	DefaultSweepInterval = 60 * time.Second
	DefaultBlockedTimeout = 30 * time.Minute
)

// Sweeper periodically self-heals stuck workflow steps.
//
// Construct via NewSweeper; Run blocks until ctx cancels. A nil enabled
// callback (used by tests) means "always on" — production wiring passes a
// per-workspace workflow_engine flag check so flag-off workspaces are
// skipped (AC5: flag-off → sweeper no-op for that workspace).
type Sweeper struct {
	engine         *Engine
	enabled        func(workspaceID string) bool // nil = always on (tests)
	interval       time.Duration
	blockedTimeout time.Duration
}

// NewSweeper constructs a Sweeper wired to an engine. enabled is the
// per-workspace workflow_engine flag check (match workflow_listeners.go's
// enabled() closure shape); nil is "always on" for tests.
func NewSweeper(engine *Engine, enabled func(workspaceID string) bool) *Sweeper {
	return &Sweeper{
		engine:         engine,
		enabled:        enabled,
		interval:       DefaultSweepInterval,
		blockedTimeout: DefaultBlockedTimeout,
	}
}

// WithInterval overrides the tick interval (tests use millisecond values
// to keep tick latency out of the test budget).
func (s *Sweeper) WithInterval(d time.Duration) *Sweeper {
	s.interval = d
	return s
}

// WithBlockedTimeout overrides the blocked→pause threshold.
func (s *Sweeper) WithBlockedTimeout(d time.Duration) *Sweeper {
	s.blockedTimeout = d
	return s
}

// Run ticks sweepOnce at s.interval until ctx is cancelled. Tick errors
// are logged inside sweepOnce and do not stop the loop — a transient DB
// hiccup must not silence the sweeper until process exit.
func (s *Sweeper) Run(ctx context.Context) error {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			s.sweepOnce(ctx)
		}
	}
}

// sweepOnce runs one tick: pull candidate steps via
// ListStepInstancesNeedingSweep, classify each by the matched condition,
// and route to the matching self-heal path. Steps in flag-off workspaces
// are skipped (AC5).
func (s *Sweeper) sweepOnce(ctx context.Context) {
	blockedSecs := int32(s.blockedTimeout / time.Second)
	candidates, err := s.engine.Queries.ListStepInstancesNeedingSweep(ctx, blockedSecs)
	if err != nil {
		slog.Warn("workflow sweeper: list candidates failed", "error", err)
		return
	}
	if len(candidates) == 0 {
		return
	}
	for _, step := range candidates {
		if !s.workspaceEnabled(ctx, step.RunID) {
			continue
		}
		// Classify by priority: blocked > deadline > taskless. A step
		// matching multiple conditions is routed by the most severe one
		// (a blocked run is never improved by re-dispatching into the
		// same wedge; a deadline-expired step is never improved by
		// re-dispatching when its issue is also taskless).
		switch {
		case step.Status == StepBlocked:
			s.handleBlockedTimeout(ctx, step)
		case step.Status == StepRunning && step.DeadlineAt.Valid && step.DeadlineAt.Time.Before(time.Now()):
			s.handleDeadlineExpired(ctx, step)
		case !step.AgentTaskID.Valid:
			s.handleMissingTask(ctx, step)
		default:
			// Race: row changed between SELECT and classification. Skip;
			// the next tick re-evaluates from the fresh DB state.
		}
	}
}

// workspaceEnabled reports whether the workflow_engine flag is on for the
// step's workspace. A nil enabled callback (tests) means "always on".
// Transient DB errors loading the run fall to "skip this tick" — a flag
// decision under a DB hiccup could either over-fire (skip flag-off check)
// or under-fire (skip flag-on workspace); skip is the conservative choice.
func (s *Sweeper) workspaceEnabled(ctx context.Context, runID pgtype.UUID) bool {
	if s.enabled == nil {
		return true
	}
	run, err := s.engine.Queries.GetWorkflowRun(ctx, runID)
	if err != nil {
		slog.Warn("workflow sweeper: load run for flag check failed",
			"run_id", util.UUIDToString(runID), "error", err)
		return false
	}
	return s.enabled(util.UUIDToString(run.WorkspaceID))
}

// handleMissingTask implements case ①: re-dispatch an active step that
// lost its agent_task_id. The pre-activation check inside
// reactivateActiveStep confirms (under the run row lock) that the step is
// still active + taskless; if another path landed a task in the meantime,
// the re-dispatch is a no-op. Engine.activateNode owns the side effects
// (issue create, EnqueueTaskForIssueWithHandoff, dispatch link, todo
// flip) so this sweeper path stays out of agent_task_queue entirely.
func (s *Sweeper) handleMissingTask(ctx context.Context, step db.StepInstance) {
	run, snap, node, ok := s.loadSweepTarget(ctx, step)
	if !ok {
		return
	}
	slog.Info("workflow sweeper: re-dispatching active step with no task",
		"run_id", util.UUIDToString(run.ID),
		"node_key", step.NodeKey,
		"step_instance_id", util.UUIDToString(step.ID))
	if err := s.reactivateActiveStep(ctx, run, snap, node, step); err != nil {
		slog.Warn("workflow sweeper: re-dispatch failed",
			"run_id", util.UUIDToString(run.ID),
			"node_key", step.NodeKey, "error", err)
	}
}

// handleDeadlineExpired implements case ②: reset a running step whose
// deadline_at passed and re-dispatch. Reset is guarded against the
// expected 'running' status; a row that already moved on (verdict landed,
// sweeper race) is a no-op.
//
// Forward-compatible hook: deadline_at is not yet populated by the engine
// in P1 (the column exists in the schema but activateAgentNode does not
// set it). The sweeper handles it correctly TODAY so a future change that
// starts populating deadline_at (e.g. node-level timeouts in P2) gets
// self-healing for free.
func (s *Sweeper) handleDeadlineExpired(ctx context.Context, step db.StepInstance) {
	run, snap, node, ok := s.loadSweepTarget(ctx, step)
	if !ok {
		return
	}
	slog.Info("workflow sweeper: deadline expired, resetting step",
		"run_id", util.UUIDToString(run.ID),
		"node_key", step.NodeKey,
		"step_instance_id", util.UUIDToString(step.ID),
		"deadline_at", step.DeadlineAt.Time)
	if err := s.resetAndReactivate(ctx, run, snap, node, step, StepRunning); err != nil {
		slog.Warn("workflow sweeper: deadline reset failed",
			"run_id", util.UUIDToString(run.ID),
			"node_key", step.NodeKey, "error", err)
	}
}

// handleBlockedTimeout implements case ③: pause a run whose blocked step
// has lingered past BlockedTimeout. Normally a step → blocked transition
// also pauses the run inside the same consumeVerdictTx / failActivation
// tx; this branch handles the inconsistent state where the step reached
// blocked but the run never followed (crash window between the step
// transition commit and the run status update, or a manual resume that
// left the blocked step in place). Pause is guarded: a run that already
// moved off running is a no-op.
func (s *Sweeper) handleBlockedTimeout(ctx context.Context, step db.StepInstance) {
	run, _, _, ok := s.loadSweepTarget(ctx, step)
	if !ok {
		return
	}
	tx, err := s.engine.TxStarter.Begin(ctx)
	if err != nil {
		slog.Warn("workflow sweeper: begin tx for blocked pause failed", "error", err)
		return
	}
	defer tx.Rollback(ctx)
	qtx := s.engine.Queries.WithTx(tx)

	locked, err := qtx.GetWorkflowRunForUpdate(ctx, run.ID)
	if err != nil {
		slog.Warn("workflow sweeper: lock run for blocked pause failed", "error", err)
		return
	}
	if locked.Status != RunRunning {
		return // run moved on (paused/cancelled/completed) between SELECT and lock — respect it
	}
	paused := s.engine.pauseRunTx(ctx, qtx, locked)
	if err := tx.Commit(ctx); err != nil {
		slog.Warn("workflow sweeper: commit blocked pause failed", "error", err)
		return
	}
	if paused {
		s.engine.publishRunUpdated(locked, RunPaused)
		s.engine.notifyInitiator(ctx, locked, "workflow_blocked", "action_required",
			fmt.Sprintf("Workflow paused: node %q has been blocked for over %s", step.NodeKey, s.blockedTimeout),
			map[string]any{
				"run_id":           util.UUIDToString(locked.ID),
				"node_key":         step.NodeKey,
				"step_instance_id": util.UUIDToString(step.ID),
			})
	}
}

// loadSweepTarget resolves (run, snapshot, node) for a candidate step.
// Returns ok=false on any precondition mismatch (run not running, corrupt
// snapshot, node missing) — the caller treats those as "skip this tick";
// the row will either be gone next tick or surface as a different case.
func (s *Sweeper) loadSweepTarget(ctx context.Context, step db.StepInstance) (db.WorkflowRun, *Snapshot, *SnapshotNode, bool) {
	run, err := s.engine.Queries.GetWorkflowRun(ctx, step.RunID)
	if err != nil {
		slog.Warn("workflow sweeper: load run failed",
			"run_id", util.UUIDToString(step.RunID), "error", err)
		return db.WorkflowRun{}, nil, nil, false
	}
	if run.Status != RunRunning {
		// The SQL filters wr.status='running' but a race between ticks
		// (or a concurrent verdict) can flip it before we re-read. Skip.
		return db.WorkflowRun{}, nil, nil, false
	}
	snap, err := ParseSnapshot(run.TemplateSnapshot)
	if err != nil {
		slog.Warn("workflow sweeper: parse snapshot failed",
			"run_id", util.UUIDToString(run.ID), "error", err)
		return db.WorkflowRun{}, nil, nil, false
	}
	node := snap.NodeByKey(step.NodeKey)
	if node == nil {
		slog.Warn("workflow sweeper: node missing from snapshot",
			"run_id", util.UUIDToString(run.ID), "node_key", step.NodeKey)
		return db.WorkflowRun{}, nil, nil, false
	}
	return run, snap, node, true
}

// reactivateActiveStep re-dispatches an active, taskless step. Mirrors
// Engine.activateNode's contract: state checks in tx (so a concurrent
// dispatcher wins the race cleanly), dispatch side effects post-commit
// (so a daemon-side failure rolls back cleanly via failActivation).
func (s *Sweeper) reactivateActiveStep(ctx context.Context, run db.WorkflowRun, snap *Snapshot, node *SnapshotNode, step db.StepInstance) error {
	tx, err := s.engine.TxStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := s.engine.Queries.WithTx(tx)

	locked, err := qtx.GetStepInstanceForUpdate(ctx, step.ID)
	if err != nil {
		return fmt.Errorf("lock step: %w", err)
	}
	if locked.Status != StepActive || locked.AgentTaskID.Valid {
		// Lost the race: another sweeper tick, engine transition, or
		// manual fix already landed a task. No-op.
		return nil
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return s.engine.activateNode(ctx, run, snap, node, step, nil)
}

// resetAndReactivate moves a stale running step back to pending and
// re-dispatches. The reset is guarded against expectedStatus so a row
// that already moved on (verdict landed, concurrent sweeper) is a no-op.
// After the guarded reset, activateStepTx promotes the same row pending →
// active (refreshing started_at); activateNode then dispatches a fresh
// task. The stale agent_task_id / issue_id on the step row are
// overwritten by UpdateStepInstanceDispatch on successful dispatch —
// orphaning the old task (reaped by runtime_sweeper's running-timeout
// arm) and the old issue (lingers in backlog; pre-existing orphan-issue
// concern for any crashed dispatch path, not specific to the sweeper).
func (s *Sweeper) resetAndReactivate(ctx context.Context, run db.WorkflowRun, snap *Snapshot, node *SnapshotNode, step db.StepInstance, expectedStatus string) error {
	tx, err := s.engine.TxStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := s.engine.Queries.WithTx(tx)

	// Serialize against in-flight verdict consumption (blueprint §8.1).
	if _, err := qtx.GetWorkflowRunForUpdate(ctx, run.ID); err != nil {
		return fmt.Errorf("lock run: %w", err)
	}
	locked, err := qtx.GetStepInstanceForUpdate(ctx, step.ID)
	if err != nil {
		return fmt.Errorf("lock step: %w", err)
	}
	if locked.Status != expectedStatus {
		return nil // moved on; respect it
	}
	// Reset the stale status → pending on the SAME row (attempt N).
	// COALESCE keeps started_at; activateStepTx's pending→active flip
	// below refreshes it via UpdateStepInstanceStatus(StartedAt=now).
	if _, err := qtx.UpdateStepInstanceStatus(ctx, db.UpdateStepInstanceStatusParams{
		NewStatus:      StepPending,
		ID:             step.ID,
		ExpectedStatus: expectedStatus,
	}); err != nil {
		return fmt.Errorf("reset step to pending: %w", err)
	}
	// Promote pending → active on the same row (writes started_at=now,
	// step_transition row). The post-commit activateNode dispatches a
	// fresh task linked to this step.
	freshStep, err := activateStepTx(ctx, qtx, run.ID, step.NodeKey)
	if err != nil {
		return fmt.Errorf("re-activate step: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	s.engine.publishStepUpdated(run, step.ID, StepActive)
	return s.engine.activateNode(ctx, run, snap, node, freshStep, nil)
}
