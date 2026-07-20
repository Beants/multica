package workflow

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// activate.go — node activation (design.md §4.1). The step row already
// exists in 'active' status (created inside the caller's transaction); this
// file runs the post-commit dispatch flow per node type:
//
//   - agent:      child issue (backlog) → EnqueueTaskForIssueWithHandoff →
//     todo flip via the service layer. The backlog-first order is a hard
//     constraint: backlog is the only status that skips
//     maybeEnqueueOnAssign, so the explicit handoff enqueue stays the single
//     dispatch path (R2 review).
//   - acceptance: pending acceptance row + run → waiting_acceptance +
//     reviewer inbox (§4.5). auto_pass is honored when the node opts in.
//   - end:        run completion (§4.5).
//   - fan_out:    pure splitter (P1-1 Wave 2). Reads upstream
//     submission.exit_fields[items_field], expands N child step rows +
//     child issues + agent dispatches inside one tx, then transitions
//     itself to passed. See fanout.go.
//   - converge:   pure AND-join (P1-1 Wave 2). Flips itself to pending
//     and waits for the upstream fan_out's children to reach terminal
//     outcomes; convergence fires from handleChildStepTerminal in
//     consumeVerdictTx. See converge.go.
//   - gate:       synchronous script gate (P1-3 MVP). Server-side script
//     execution under a double-transaction boundary; transitions itself
//     to passed/blocked and reuses runSignalAction for post-commit
//     downstream advance. See gate.go.

// activateNode dispatches one already-active step by node type.
// reworkCtx is non-nil only on rework rounds (D-8 explicit injection).
func (e *Engine) activateNode(ctx context.Context, run db.WorkflowRun, snap *Snapshot, node *SnapshotNode, step db.StepInstance, reworkCtx *ReworkContext) error {
	switch node.Type {
	case NodeTypeAgent:
		return e.activateAgentNode(ctx, run, snap, node, step, reworkCtx, false)
	case NodeTypeAcceptance:
		return e.activateAcceptanceNode(ctx, run, node, step)
	case NodeTypeEnd:
		return e.activateEndNode(ctx, run, step)
	case NodeTypeFanOut:
		return e.activateFanOutNode(ctx, run, snap, node, step)
	case NodeTypeConverge:
		return e.activateConvergeNode(ctx, run, snap, node, step)
	case NodeTypeGate:
		return e.activateGateNode(ctx, run, snap, node, step)
	default:
		return fmt.Errorf("workflow: unsupported P0 node type %q", node.Type)
	}
}

// activateEndNode closes out the chain (§4.5): the end step passes (so the
// run timeline shows every node terminal) and the run completes.
func (e *Engine) activateEndNode(ctx context.Context, run db.WorkflowRun, step db.StepInstance) error {
	tx, err := e.TxStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := e.Queries.WithTx(tx)
	locked, err := qtx.GetStepInstanceForUpdate(ctx, step.ID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("lock end step: %w", err)
	}
	passed := false
	if err == nil && !isTerminalStepStatus(locked.Status) {
		passed = e.transitionStepTx(ctx, qtx, locked, StepPassed, "engine", nil)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	if passed {
		e.publishStepUpdated(run, step.ID, StepPassed)
	}
	return e.completeRun(ctx, run)
}

// activateAgentNode runs the hard-ordered activation sequence:
//  1. create the node child issue in 'backlog' (skips the auto-enqueue),
//  2. explicit EnqueueTaskForIssueWithHandoff carrying the node context,
//  3. link dispatch artifacts onto the step,
//  4. flip the child issue to todo through the SERVICE layer so the
//     event/activity/WS side effects fire (R3 review #9 — never raw sqlc).
//
// P1-3b: adversarial is true only when called from activateGateAgentNode
// with gate_type=adversarial. It signals buildHandoffNote to apply the
// adversarial context whitelist (squad-briefing.md:158).
func (e *Engine) activateAgentNode(ctx context.Context, run db.WorkflowRun, snap *Snapshot, node *SnapshotNode, step db.StepInstance, reworkCtx *ReworkContext, adversarial bool) error {
	agentID, err := util.ParseUUID(node.Config.AgentID)
	if err != nil {
		return e.failActivation(ctx, run, step, fmt.Errorf("node %q has no frozen agent_id (republish the template)", node.NodeKey))
	}

	intakeNumber := int64(0)
	if run.IntakeIssueID.Valid {
		if intake, ierr := e.Queries.GetIssue(ctx, run.IntakeIssueID); ierr == nil {
			intakeNumber = int64(intake.Number)
		}
	}
	// <runNumber>-<node_key>-attempt<N> — the attempt suffix keeps titles
	// unique across retry/rework rounds, sidestepping the active-duplicate
	// title guard (design.md §4.1).
	title := fmt.Sprintf("%d-%s-attempt%d", intakeNumber, node.NodeKey, step.Attempt)

	note := e.buildHandoffNote(ctx, run, snap, node, step, reworkCtx, adversarial)

	// initiator stays raw for attribution (invalid => owner_fallback /
	// fail-closed per MUL-4302); creator is coerced to the zero-UUID
	// system-actor convention for issue CreatorID.
	initiator := ParseRunContext(run.Context).Initiator()
	creator := initiator
	if !creator.Valid {
		creator = pgtype.UUID{Valid: true} // system-actor zero UUID convention
	}
	res, err := e.Issues.Create(ctx, service.IssueCreateParams{
		WorkspaceID:   run.WorkspaceID,
		Title:         title,
		Status:        "backlog",
		Priority:      "none",
		AssigneeType:  pgtype.Text{String: "agent", Valid: true},
		AssigneeID:    agentID,
		CreatorType:   "member",
		CreatorID:     creator,
		ParentIssueID: run.IntakeIssueID,
		// Defense in depth: the title is unique by construction, but a
		// re-activated step must never hard-fail on a stale same-title row.
		AllowDuplicate: true,
	}, service.IssueCreateOpts{})
	if err != nil {
		return e.failActivation(ctx, run, step, fmt.Errorf("create node issue: %w", err))
	}
	issue := res.Issue

	task, err := e.Tasks.EnqueueTaskForIssueWithHandoff(ctx, issue, note, initiator)
	if err != nil {
		return e.failActivation(ctx, run, step, fmt.Errorf("enqueue node task: %w", err))
	}

	if _, err := e.Queries.UpdateStepInstanceDispatch(ctx, db.UpdateStepInstanceDispatchParams{
		ID:          step.ID,
		AgentID:     agentID,
		AgentTaskID: task.ID,
		IssueID:     issue.ID,
	}); err != nil {
		slog.Error("workflow: link dispatch artifacts failed",
			"step_instance_id", util.UUIDToString(step.ID), "error", err)
	}

	if _, err := e.Issues.SetStatus(ctx, issue, "todo"); err != nil {
		slog.Error("workflow: promote node issue to todo failed",
			"issue_id", util.UUIDToString(issue.ID), "error", err)
	}
	return nil
}

// failActivation parks a step whose dispatch failed (no runtime, archived
// agent, …) instead of leaving it silently active: step → blocked, run →
// paused, initiator notified — the same landing as a blocked verdict.
func (e *Engine) failActivation(ctx context.Context, run db.WorkflowRun, step db.StepInstance, cause error) error {
	slog.Warn("workflow: activation failed",
		"run_id", util.UUIDToString(run.ID),
		"step_instance_id", util.UUIDToString(step.ID),
		"node_key", step.NodeKey, "error", cause)
	tx, terr := e.TxStarter.Begin(ctx)
	blocked, paused := false, false
	if terr == nil {
		qtx := e.Queries.WithTx(tx)
		if locked, lerr := qtx.GetStepInstanceForUpdate(ctx, step.ID); lerr == nil && !isTerminalStepStatus(locked.Status) {
			if e.transitionStepTx(ctx, qtx, locked, StepBlocked, "system", map[string]any{"reason": cause.Error()}) {
				blocked = true
				paused = e.pauseRunTx(ctx, qtx, run)
			}
		}
		if cerr := tx.Commit(ctx); cerr != nil {
			terr = cerr
		} else {
			terr = nil
		}
	}
	if terr != nil {
		slog.Error("workflow: activation failure state write failed", "error", terr)
	} else {
		if blocked {
			e.publishStepUpdated(run, step.ID, StepBlocked)
		}
		if paused {
			e.publishRunUpdated(run, RunPaused)
		}
	}
	e.notifyInitiator(ctx, run, "workflow_blocked", "action_required",
		fmt.Sprintf("Workflow paused: could not dispatch node %q (%s)", step.NodeKey, cause.Error()),
		map[string]any{"run_id": util.UUIDToString(run.ID), "node_key": step.NodeKey})
	return cause
}

// activateAcceptanceNode parks the run on a human decision (design.md §4.5):
// create the pending acceptance bound to this step, run →
// waiting_acceptance, notify the reviewer. auto_pass (node capability,
// default OFF — D-12 ruling) short-circuits when every upstream step passed.
func (e *Engine) activateAcceptanceNode(ctx context.Context, run db.WorkflowRun, node *SnapshotNode, step db.StepInstance) error {
	reviewer := ParseRunContext(run.Context).Reviewer()
	if !reviewer.Valid && node.Config.ReviewerID != "" {
		// Template-level default reviewer. A malformed UUID degrades to "no
		// reviewer" (the acceptance still parks the run; nobody is pinged) —
		// publish-time validation owns config shape.
		if parsed, err := util.ParseUUID(node.Config.ReviewerID); err == nil {
			reviewer = parsed
		}
	}

	tx, err := e.TxStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := e.Queries.WithTx(tx)

	acc, err := qtx.CreateAcceptance(ctx, db.CreateAcceptanceParams{
		RunID:          run.ID,
		StepInstanceID: step.ID,
		ReviewerID:     reviewer,
	})
	if err != nil {
		if isUniqueViolation(err) {
			// idx_acceptance_pending_step: a pending acceptance already
			// exists for this step — duplicate activation is a no-op.
			_ = tx.Rollback(ctx)
			return nil
		}
		return fmt.Errorf("create acceptance: %w", err)
	}
	if _, err := qtx.UpdateWorkflowRunStatus(ctx, db.UpdateWorkflowRunStatusParams{
		NewStatus: RunWaitingAcceptance, ID: run.ID, ExpectedStatus: RunRunning,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // run moved on (paused/cancelled) — respect it
		}
		return fmt.Errorf("park run for acceptance: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	e.publishRunUpdated(run, RunWaitingAcceptance)

	// auto_pass: explicit node capability AND every upstream step passed
	// (D-12). Default off — the seeds keep final acceptance human. The
	// invalid decider UUID marks the system path (design.md §4.5).
	if node.Config.AutoPass && e.allUpstreamPassed(ctx, run.ID, node.NodeKey) {
		if err := e.ApproveAcceptance(ctx, run.ID, acc.ID, pgtype.UUID{}); err != nil {
			slog.Warn("workflow: auto_pass approve failed", "acceptance_id", util.UUIDToString(acc.ID), "error", err)
		}
		return nil
	}

	if reviewer.Valid {
		if member, err := e.Queries.GetMember(ctx, reviewer); err == nil {
			e.notifyMember(ctx, run, member.UserID, "workflow_acceptance", "action_required",
				fmt.Sprintf("Acceptance requested: %s (%s)", node.Name, intakeTitle(ctx, e.Queries, run)),
				map[string]any{"node_key": node.NodeKey, "acceptance_id": util.UUIDToString(acc.ID)})
		} else {
			slog.Warn("workflow: acceptance reviewer not found", "reviewer_id", util.UUIDToString(reviewer), "error", err)
		}
	}
	return nil
}

// allUpstreamPassed reports whether every node before nodeKey in the chain
// has its latest step passed — the auto_pass condition (design.md §4.5).
func (e *Engine) allUpstreamPassed(ctx context.Context, runID pgtype.UUID, nodeKey string) bool {
	run, err := e.Queries.GetWorkflowRun(ctx, runID)
	if err != nil {
		return false
	}
	snap, err := ParseSnapshot(run.TemplateSnapshot)
	if err != nil {
		return false
	}
	for _, upKey := range snap.UpstreamNodeKeys(nodeKey) {
		latest, err := e.Queries.GetLatestStepInstanceForNode(ctx, db.GetLatestStepInstanceForNodeParams{
			RunID: runID, NodeKey: upKey,
		})
		if err != nil || latest.Status != StepPassed {
			return false
		}
	}
	return true
}
