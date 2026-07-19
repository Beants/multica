package workflow

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// task_events.go — daemon task lifecycle → engine mapping (design.md §4.4).
// The daemon only knows agent_task ids; step_instance.agent_task_id is the
// join key. Every entry point is a no-op for tasks not bound to a workflow
// step (the overwhelming majority of task traffic), for terminal steps (late
// events racing the verdict path), and for runs that left an active status.
// All transitions are guarded and write step_transition rows (§4.6); the
// trigger is "system" (the CHECK constraint's bucket for non-verdict
// automation) with the source event named in the payload.

// stepForTask resolves the step bound to an agent task. found=false means a
// regular (non-workflow) task — callers no-op quietly.
func (e *Engine) stepForTask(ctx context.Context, taskID pgtype.UUID) (db.StepInstance, bool, error) {
	step, err := e.Queries.GetStepInstanceByTask(ctx, taskID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.StepInstance{}, false, nil
		}
		return db.StepInstance{}, false, fmt.Errorf("get step by task: %w", err)
	}
	return step, true, nil
}

// HandleTaskDispatch maps task:dispatch (a daemon claimed the task) onto the
// bound step: active → dispatched (§4.4). Any other current status makes the
// event a no-op — a duplicate claim event, or a late one after a verdict
// already terminated the step.
func (e *Engine) HandleTaskDispatch(ctx context.Context, taskID pgtype.UUID) error {
	step, found, err := e.stepForTask(ctx, taskID)
	if err != nil || !found {
		return err
	}
	if step.Status != StepActive {
		return nil
	}

	tx, err := e.TxStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := e.Queries.WithTx(tx)
	locked, err := qtx.GetStepInstanceForUpdate(ctx, step.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("lock step: %w", err)
	}
	if locked.Status != StepActive {
		return nil
	}
	e.transitionStepTx(ctx, qtx, locked, StepDispatched, "system", map[string]any{
		"event":   "task:dispatch",
		"task_id": util.UUIDToString(taskID),
	})
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	// This handler never otherwise reads the run; fetch it for the event
	// payload. A failed read skips the event — best-effort, never fatal.
	if run, rerr := e.Queries.GetWorkflowRun(ctx, locked.RunID); rerr == nil {
		e.publishStepUpdated(run, locked.ID, StepDispatched)
	} else {
		slog.Warn("workflow: load run for dispatch event failed",
			"run_id", util.UUIDToString(locked.RunID), "error", rerr)
	}
	return nil
}

// HandleTaskFailed maps task:failed (daemon crash / runtime offline) onto the
// bound step (§4.4): the step fails and the node policy decides the landing —
// a fresh attempt while attempt < max_attempts, otherwise the run pauses and
// the intake issue escalates to the human initiator. Mirrors the verdict-fail
// branch of consumeVerdictTx, minus exit-field copying (there is none).
func (e *Engine) HandleTaskFailed(ctx context.Context, taskID pgtype.UUID) error {
	step, found, err := e.stepForTask(ctx, taskID)
	if err != nil || !found {
		return err
	}
	if isTerminalStepStatus(step.Status) {
		return nil
	}

	var (
		run   db.WorkflowRun
		snap  *Snapshot
		node  *SnapshotNode
		fresh db.StepInstance
		retry bool
	)

	tx, err := e.TxStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := e.Queries.WithTx(tx)

	locked, err := qtx.GetStepInstanceForUpdate(ctx, step.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("lock step: %w", err)
	}
	if isTerminalStepStatus(locked.Status) {
		return nil
	}
	// Serialize with verdict consumption / acceptance decisions (§8.1).
	run, err = qtx.GetWorkflowRunForUpdate(ctx, locked.RunID)
	if err != nil {
		return fmt.Errorf("lock run: %w", err)
	}
	if run.Status != RunRunning && run.Status != RunWaitingAcceptance {
		return nil
	}
	snap, err = ParseSnapshot(run.TemplateSnapshot)
	if err != nil {
		return err
	}
	node = snap.NodeByKey(locked.NodeKey)
	if node == nil {
		return fmt.Errorf("workflow: node %q missing from run snapshot", locked.NodeKey)
	}

	if !e.transitionStepTx(ctx, qtx, locked, StepFailed, "system", map[string]any{
		"event":   "task:failed",
		"task_id": util.UUIDToString(taskID),
	}) {
		return nil // lost the guard race; another consumer landed first
	}
	retry = locked.Attempt < node.Config.EffectiveMaxAttempts()
	if retry {
		fresh, err = newAttemptStepTx(ctx, qtx, run.ID, locked.NodeKey, locked.Attempt+1)
		if err != nil {
			return err
		}
	} else if !e.pauseRunTx(ctx, qtx, run) {
		return nil // run raced elsewhere; abandon so the step write rolls back too
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	e.publishStepUpdated(run, locked.ID, StepFailed)
	if retry {
		e.publishStepUpdated(run, fresh.ID, StepActive)
	} else {
		e.publishRunUpdated(run, RunPaused)
	}
	e.closeStepIssue(ctx, locked, "cancelled")
	if retry {
		return e.activateNode(ctx, run, snap, node, fresh, nil)
	}
	e.handoffToHuman(ctx, run,
		fmt.Sprintf("Workflow paused: node %q failed attempt %d of %d (task failed)", locked.NodeKey, locked.Attempt, node.Config.EffectiveMaxAttempts()),
		"workflow_escalated")
	return nil
}

// HandleTaskCompleted maps task:completed onto the bound step (§4.4): a task
// that finished WITHOUT leaving a submission means the agent wrapped up
// without reporting — the step blocks, the run pauses, and the initiator is
// notified. P0 minimal: the check runs at completion time (no grace timers);
// the P1 sweeper owns reconciling whatever this misses.
func (e *Engine) HandleTaskCompleted(ctx context.Context, taskID pgtype.UUID) error {
	step, found, err := e.stepForTask(ctx, taskID)
	if err != nil || !found {
		return err
	}
	if isTerminalStepStatus(step.Status) {
		return nil
	}
	has, err := stepHasSubmission(ctx, e.Queries, step.ID)
	if err != nil || has {
		return err
	}

	tx, err := e.TxStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := e.Queries.WithTx(tx)

	locked, err := qtx.GetStepInstanceForUpdate(ctx, step.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("lock step: %w", err)
	}
	if isTerminalStepStatus(locked.Status) {
		return nil
	}
	run, err := qtx.GetWorkflowRunForUpdate(ctx, locked.RunID)
	if err != nil {
		return fmt.Errorf("lock run: %w", err)
	}
	if run.Status != RunRunning && run.Status != RunWaitingAcceptance {
		return nil
	}
	// Re-check under the step lock: a submission committed between the
	// pre-check and this lock owns the step's advancement — never block out
	// from under it. (A submission committing AFTER this re-check is the
	// accepted P0 race; its resignal no-ops on the now-terminal step.)
	has, err = stepHasSubmission(ctx, qtx, locked.ID)
	if err != nil || has {
		return err
	}

	if !e.transitionStepTx(ctx, qtx, locked, StepBlocked, "system", map[string]any{
		"event":   "task:completed",
		"task_id": util.UUIDToString(taskID),
		"reason":  "task completed without submission",
	}) {
		return nil
	}
	if !e.pauseRunTx(ctx, qtx, run) {
		return nil
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	e.publishStepUpdated(run, locked.ID, StepBlocked)
	e.publishRunUpdated(run, RunPaused)
	e.notifyInitiator(ctx, run, "workflow_blocked", "action_required",
		fmt.Sprintf("Workflow paused: node %q finished without a submission", locked.NodeKey),
		map[string]any{"run_id": util.UUIDToString(run.ID), "node_key": locked.NodeKey})
	return nil
}

// stepHasSubmission reports whether the step already carries a submission —
// the signal that the normal verdict path owns the step's advancement.
func stepHasSubmission(ctx context.Context, q *db.Queries, stepID pgtype.UUID) (bool, error) {
	_, err := q.GetSubmissionByStepInstance(ctx, stepID)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return false, fmt.Errorf("check submission: %w", err)
}
