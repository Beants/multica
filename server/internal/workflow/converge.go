package workflow

// converge.go — P1-1 Wave 2 converge node activation + child-step
// terminal handler (design.md §2.3, §4.1; PRD R5).
//
// converge is a pure AND-join: at activation it parks in pending and
// waits for every sibling under the upstream fan_out's parent_step_id
// to reach a terminal outcome. When the last outcome lands, the
// handler decides the converge's next state from the fan_out's
// fail_policy: pass on all-passed, blocked on policy=blocked, or
// hand the failed child to reworkChildStepScope on policy=rework.

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

// convergeEffectKind enumerates the post-commit side effects a
// handleChildStepTerminal run may queue. The empty value
// ("held" sentinel) means "no decision yet — converge still waits."
const (
	convergeKindHeld     = "held"     // no transition; waiting for more children
	convergeKindPassed   = "passed"   // all children passed → converge passed + downstream activated
	convergeKindFailed   = "failed"   // policy=fail: siblings skipped, run failed
	convergeKindBlocked  = "blocked"  // policy=blocked: converge+run blocked
	convergeKindReworked = "reworked" // policy=rework: failed child got a new attempt
)

// convergeEffect describes the post-commit side effects of one
// handleChildStepTerminal invocation. It is produced inside the
// caller's transaction (converge transitions, sibling skips, child
// rework attempts all commit atomically with the original child
// transition) and consumed by runSignalAction after commit.
type convergeEffect struct {
	kind             string
	convergeStepID   pgtype.UUID      // for publishStepUpdated
	convergeStatus   string           // new converge status (passed/blocked)
	downstreamNode   *SnapshotNode    // kind=passed: post-converge node to activate
	downstreamStep   *db.StepInstance // kind=passed: its fresh step row
	skippedSiblings  []pgtype.UUID    // kind=failed: sibling step IDs skipped (events only)
	fanOutNode       *SnapshotNode    // kind=reworked: parent fan_out (for handoff note)
	branchNode       *SnapshotNode    // kind=reworked: child branch node
	reworkTargetStep db.StepInstance  // kind=reworked: failed child step to re-enter
	run              db.WorkflowRun
}

// activateConvergeNode is the converge dispatcher (design.md §2.3):
// at activation the converge step is parked in pending — it has no
// agent task of its own. Convergence fires later from
// handleChildStepTerminal when the last fan_out child reaches a
// terminal outcome.
//
// The step row arrives in 'active' (activateStepTx promotes pending
// → active before dispatching). activateConvergeNode reverses that
// to 'pending' so the semantic — "waiting for children" — matches
// the stored status. The transition is recorded for audit.
func (e *Engine) activateConvergeNode(ctx context.Context, run db.WorkflowRun, snap *Snapshot, node *SnapshotNode, step db.StepInstance) error {
	tx, err := e.TxStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := e.Queries.WithTx(tx)

	if _, err := qtx.GetWorkflowRunForUpdate(ctx, run.ID); err != nil {
		return fmt.Errorf("lock run for converge: %w", err)
	}

	// Re-read the step under the converge tx so the guard sees the
	// post-active status (activateStepTx already wrote active).
	locked, err := qtx.GetStepInstanceForUpdate(ctx, step.ID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("lock converge step: %w", err)
	}
	if err == nil && locked.Status == StepActive {
		// active → pending. Use transitionStepTx for the audit row.
		e.transitionStepTx(ctx, qtx, locked, StepPending, "engine", map[string]any{
			"converge": node.NodeKey,
		})
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit converge activate: %w", err)
	}
	e.publishStepUpdated(run, step.ID, StepPending)
	return nil
}

// handleChildStepTerminal is the convergence trigger (design.md §2.3):
// called by consumeVerdictTx after a fan_out child step's terminal
// transition succeeds. Under the run row lock it counts every child's
// outcome, applies the fan_out's fail_policy, and — when all children
// pass — flips the converge to passed and activates the post-converge
// downstream step. The returned convergeEffect drives post-commit
// event emission + downstream node activation.
//
// Policy trigger matrix (design.md §2.3 + §4.2):
//   - FailPolicyFail / FailPolicyBlocked: fire on any failed OR blocked
//     child (both count as a non-pass outcome for these policies).
//   - FailPolicyRework: fire on failed only. A BLOCKED child under
//     policy=rework has no rework to do (the agent reported a stall,
//     not a failure) → return held so the caller's P0 pause path
//     handles the blocked verdict normally.
//
// Returns (nil, nil) when the policy keeps converge waiting for more
// children. Real errors roll the caller's transaction back through
// the returned error.
func (e *Engine) handleChildStepTerminal(ctx context.Context, qtx *db.Queries, run db.WorkflowRun, childStep db.StepInstance, newStatus string) (*convergeEffect, error) {
	if !childStep.ParentStepID.Valid {
		return nil, nil // not a fan_out child — nothing to converge
	}

	// Locate the parent fan_out step + the converge step downstream.
	fanOutStep, err := qtx.GetStepInstance(ctx, childStep.ParentStepID)
	if err != nil {
		return nil, fmt.Errorf("load fan_out parent step: %w", err)
	}
	snapForFanOut, err := ParseSnapshot(run.TemplateSnapshot)
	if err != nil {
		return nil, err
	}
	fanOutNode := snapForFanOut.NodeByKey(fanOutStep.NodeKey)
	if fanOutNode == nil {
		return nil, fmt.Errorf("workflow: fan_out node %q missing from snapshot", fanOutStep.NodeKey)
	}
	branchNode := snapForFanOut.NodeByKey(childStep.NodeKey)
	if branchNode == nil {
		// Synthetic / unknown child node key — bail safely.
		return nil, fmt.Errorf("workflow: child step node %q missing from snapshot", childStep.NodeKey)
	}
	convergeNode := firstConvergeDownstream(snapForFanOut, branchNode)
	if convergeNode == nil {
		// Template pairing broken — no converge to drive. Log and bail.
		slog.Warn("workflow: fan_out child has no downstream converge",
			"run_id", util.UUIDToString(run.ID), "branch_node", branchNode.NodeKey)
		return nil, nil
	}

	// Re-lock the run inside this caller's tx (idempotent under same tx).
	if _, err := qtx.GetWorkflowRunForUpdate(ctx, run.ID); err != nil {
		return nil, fmt.Errorf("lock run for converge check: %w", err)
	}

	// Fan_out's fail_policy (default rework via EffectiveFailPolicy).
	policy := fanOutNode.Config.EffectiveFailPolicy()

	// Tally child outcomes under this fan_out.
	all, err := qtx.ListStepInstancesForRun(ctx, run.ID)
	if err != nil {
		return nil, fmt.Errorf("list steps for converge tally: %w", err)
	}
	var (
		passed, failed, blocked, pending, inFlight int
		latestPerSlot                               = map[int]db.StepInstance{}
	)
	for _, s := range all {
		if !s.ParentStepID.Valid || util.UUIDToString(s.ParentStepID) != util.UUIDToString(fanOutStep.ID) {
			continue
		}
		// Track the latest step per child slot so rework attempts
		// don't double-count (an old failed attempt + the new active
		// attempt would otherwise both be in the tally).
		slot := int(s.Attempt) / childAttemptSlot
		if cur, ok := latestPerSlot[slot]; !ok || s.Attempt > cur.Attempt {
			latestPerSlot[slot] = s
		}
	}
	for _, s := range latestPerSlot {
		switch s.Status {
		case StepPassed:
			passed++
		case StepFailed:
			failed++
		case StepBlocked:
			blocked++
		case StepRework:
			// Old attempt superseded by a fresh active step in the same
			// slot — should not appear in latestPerSlot because the new
			// attempt has a higher attempt number. Count as in-flight
			// if it sneaks through.
			inFlight++
		case StepActive, StepDispatched, StepRunning:
			inFlight++
		case StepPending:
			pending++
		case StepSkipped:
			// Sibling skipped via fail_policy=fail. Treat as a terminal
			// non-passed outcome so the converge does not stall on it.
			failed++
		}
	}
	totalChildren := len(latestPerSlot)

	// Outcome decision matrix.
	eff := &convergeEffect{kind: convergeKindHeld, run: run}

	// Decide whether the policy should fire on this outcome. Under
	// policy=rework, a BLOCKED verdict is NOT a fail signal (the agent
	// reported a stall, not bad output) — defer to P0's normal pause
	// path. Under fail/blocked policies, both failed and blocked
	// children trip the policy.
	policyTriggers := false
	switch policy {
	case FailPolicyFail, FailPolicyBlocked:
		policyTriggers = failed > 0 || blocked > 0
	case FailPolicyRework:
		policyTriggers = failed > 0
	}

	switch {
	case policyTriggers:
		switch policy {
		case FailPolicyFail:
			skipped, perr := e.applyFailPolicy(ctx, qtx, run, fanOutStep, policy)
			if perr != nil {
				return nil, perr
			}
			eff.kind = convergeKindFailed
			eff.skippedSiblings = skipped
			return eff, nil

		case FailPolicyBlocked:
			if _, perr := e.applyFailPolicy(ctx, qtx, run, fanOutStep, policy); perr != nil {
				return nil, perr
			}
			// Find the converge step to publish its new blocked status.
			if conv, cerr := qtx.GetStepInstanceForNodeWithStatus(ctx, db.GetStepInstanceForNodeWithStatusParams{
				RunID: run.ID, NodeKey: convergeNode.NodeKey, Status: StepBlocked,
			}); cerr == nil {
				eff.kind = convergeKindBlocked
				eff.convergeStepID = conv.ID
				eff.convergeStatus = StepBlocked
			}
			return eff, nil

		case FailPolicyRework:
			// Re-enter only the failed child (latest attempt of its
			// slot). Do NOT touch siblings or downstream — that's the
			// whole point of reworkChildStepScope (design §4.4).
			// Caller commits the original child transition, then
			// runSignalAction calls reworkChildStepScope with the now-
			// stable target. This keeps the child transition + the
			// rework as two atomic units instead of one nested tx.
			targetSlot := int(childStep.Attempt) / childAttemptSlot
			target, ok := latestPerSlot[targetSlot]
			if !ok {
				target = childStep
			}
			eff.kind = convergeKindReworked
			eff.fanOutNode = fanOutNode
			eff.branchNode = branchNode
			eff.reworkTargetStep = target
			return eff, nil
		}

	case passed == totalChildren && totalChildren > 0:
		// All children passed → converge pending → passed, activate
		// downstream. Conditional UPDATE (status='pending') makes a
		// concurrent double-fire a no-op.
		conv, cerr := qtx.GetStepInstanceForNodeWithStatus(ctx, db.GetStepInstanceForNodeWithStatusParams{
			RunID: run.ID, NodeKey: convergeNode.NodeKey, Status: StepPending,
		})
		if errors.Is(cerr, pgx.ErrNoRows) {
			// Already moved (race); treat as held.
			return eff, nil
		}
		if cerr != nil {
			return nil, fmt.Errorf("load converge step: %w", cerr)
		}
		if !e.transitionStepTx(ctx, qtx, conv, StepPassed, "engine", map[string]any{
			"converge_all_passed": totalChildren,
		}) {
			return eff, nil // lost guard race
		}
		// Activate the post-converge downstream (P0 single-edge after converge).
		// P1-2: converge edges are catch-all per R7; nil evalCtx = topology
		// mode (every edge hits), which is what we want for a structural hop.
		afterConverge := snapForFanOut.NextAfterAll(convergeNode.NodeKey, nil)
		var downstreamNode *SnapshotNode
		var downstreamStep *db.StepInstance
		if len(afterConverge) > 0 {
			ds := afterConverge[0]
			dsStep, aerr := activateStepTx(ctx, qtx, run.ID, ds.NodeKey)
			if aerr != nil {
				return nil, fmt.Errorf("activate post-converge downstream: %w", aerr)
			}
			// lookahead for the post-converge downstream (one level).
			for _, la := range lookaheadTargets(snapForFanOut, &ds) {
				if err := preCreateStepTx(ctx, qtx, run.ID, la.NodeKey); err != nil {
					return nil, fmt.Errorf("pre-create post-converge lookahead: %w", err)
				}
			}
			downstreamNode = &ds
			downstreamStep = &dsStep
		}
		eff.kind = convergeKindPassed
		eff.convergeStepID = conv.ID
		eff.convergeStatus = StepPassed
		eff.downstreamNode = downstreamNode
		eff.downstreamStep = downstreamStep
		return eff, nil

	default:
		// Mix of passed + in-flight/pending: still waiting.
		return eff, nil
	}

	return eff, nil
}
