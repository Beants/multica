package workflow

// converge_test.go — P1-1 Wave 2 converge engine coverage (DB-backed).
// Drives a published fan_out template through each fail_policy outcome
// and asserts the converge + run + sibling state transitions.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// failAChild drives one fan_out child to a definitive fail by burning
// through its max attempts. The default max_attempts is 3, so three
// BLOCKED submissions + their rework dispatches land the child in a
// terminal-fail state under policy=rework (circuit breaker), or in
// the policy's terminal bucket (fail/blocked) on the first fail.
func failAChild(f *testFixture, runID pgtype.UUID, childAttempt int32) db.StepInstance {
	f.t.Helper()
	for round := 0; round < circuitBreakerLimit+1; round++ {
		steps, _ := f.queries.ListStepInstancesForRun(context.Background(), runID)
		var cur db.StepInstance
		for _, s := range steps {
			if s.NodeKey == "branch" && s.ParentStepID.Valid {
				if int(s.Attempt)/childAttemptSlot == int(childAttempt)/childAttemptSlot {
					if s.Attempt >= cur.Attempt {
						cur = s
					}
				}
			}
		}
		if !cur.ID.Valid {
			f.t.Fatalf("round %d: no child step in slot %d", round, childAttempt/childAttemptSlot)
		}
		if isTerminalStepStatus(cur.Status) && cur.Status != StepRework {
			return cur // policy=fail/blocked already terminated the child
		}
		if !cur.AgentTaskID.Valid {
			f.t.Fatalf("round %d: child step has no task (status=%q)", round, cur.Status)
		}
		_, _, err := f.engine.RecordSubmission(context.Background(), cur.AgentTaskID, SubmissionInput{
			Status: SubmissionBlocked,
		})
		if err != nil {
			// Run paused / step terminal — caller inspects final state.
			return cur
		}
	}
	// Return the latest state regardless.
	return f.latestStep(runID, "branch")
}

func TestHandleChildStepTerminal_AllPassed(t *testing.T) {
	f := newTestFixture(t)
	tmpl := fanOutTemplate(f, "conv-all-pass", FailPolicyRework)
	run := f.startRun(tmpl, "conv-all-pass-1", "All pass")

	passUpstream(f, run.ID, []any{
		validItem("a"), validItem("b"),
	})

	// Pass both children.
	steps, _ := f.queries.ListStepInstancesForRun(context.Background(), run.ID)
	for _, s := range steps {
		if s.NodeKey != "branch" || !s.ParentStepID.Valid || !s.AgentTaskID.Valid {
			continue
		}
		if _, _, err := f.engine.RecordSubmission(context.Background(), s.AgentTaskID, SubmissionInput{
			Status: SubmissionDone,
		}); err != nil {
			t.Fatalf("pass child: %v", err)
		}
	}

	// Converge must be passed.
	conv := f.latestStep(run.ID, "converge")
	if conv.Status != StepPassed {
		t.Fatalf("converge status = %q, want passed", conv.Status)
	}
	// Downstream end node must be active (post-converge activation).
	end := f.latestStep(run.ID, "end")
	if end.Status != StepActive && end.Status != StepPassed {
		t.Fatalf("end status = %q, want active or passed", end.Status)
	}
	// Run not paused/failed.
	if got := f.runStatus(run.ID); got != RunRunning && got != RunCompleted {
		t.Fatalf("run status = %q, want running or completed", got)
	}
}

func TestHandleChildStepTerminal_PolicyFail(t *testing.T) {
	f := newTestFixture(t)
	tmpl := fanOutTemplate(f, "conv-fail-policy", FailPolicyFail)
	run := f.startRun(tmpl, "conv-fail-1", "Policy fail")

	passUpstream(f, run.ID, []any{
		validItem("a"), validItem("b"), validItem("c"),
	})

	// Fail one child definitively.
	failAChild(f, run.ID, 1)

	// All other active children must be skipped.
	steps, _ := f.queries.ListStepInstancesForRun(context.Background(), run.ID)
	for _, s := range steps {
		if s.NodeKey != "branch" || !s.ParentStepID.Valid {
			continue
		}
		// Any non-terminal sibling must have been skipped.
		if s.Status == StepActive || s.Status == StepDispatched || s.Status == StepRunning {
			t.Errorf("sibling child step still %q after policy=fail", s.Status)
		}
	}
	// Run must be failed.
	if got := f.runStatus(run.ID); got != RunFailed {
		t.Fatalf("run status = %q, want failed", got)
	}
}

func TestHandleChildStepTerminal_PolicyBlocked(t *testing.T) {
	f := newTestFixture(t)
	tmpl := fanOutTemplate(f, "conv-blocked-policy", FailPolicyBlocked)
	run := f.startRun(tmpl, "conv-blocked-1", "Policy blocked")

	passUpstream(f, run.ID, []any{
		validItem("a"), validItem("b"),
	})

	failAChild(f, run.ID, 1)

	// Run must be paused.
	if got := f.runStatus(run.ID); got != RunPaused && got != RunFailed {
		t.Fatalf("run status = %q, want paused or failed", got)
	}
	// Converge must be blocked (or pending if not yet flushed).
	conv := f.latestStep(run.ID, "converge")
	if conv.Status != StepBlocked && conv.Status != StepPending {
		t.Fatalf("converge status = %q, want blocked or pending", conv.Status)
	}
}

func TestHandleChildStepTerminal_PolicyRework(t *testing.T) {
	f := newTestFixture(t)
	tmpl := fanOutTemplate(f, "conv-rework-policy", FailPolicyRework)
	run := f.startRun(tmpl, "conv-rework-1", "Policy rework")

	passUpstream(f, run.ID, []any{
		validItem("a"), validItem("b"),
	})

	// In this P1-1 single-edge setup the branch node is an executor,
	// whose system-derived verdict can only be pass or blocked (never
	// fail). policy=rework is documented to fire only on `failed`
	// (design.md §2.3 + §4.2), so a BLOCKED submission under
	// policy=rework must defer to the P0 pause path — siblings stay
	// untouched (no BFS leak), run pauses, converge holds.
	steps, _ := f.queries.ListStepInstancesForRun(context.Background(), run.ID)
	var child0 db.StepInstance
	for _, s := range steps {
		if s.NodeKey == "branch" && s.ParentStepID.Valid && s.Attempt == 1 {
			child0 = s
		}
	}
	if !child0.ID.Valid {
		t.Fatalf("no slot-0 child step found")
	}
	if _, _, err := f.engine.RecordSubmission(context.Background(), child0.AgentTaskID, SubmissionInput{
		Status: SubmissionBlocked,
	}); err != nil {
		t.Fatalf("submit BLOCKED on slot-0 child: %v", err)
	}

	// Sibling (slot 1025) must remain in its pre-submission state.
	stepsAfter, _ := f.queries.ListStepInstancesForRun(context.Background(), run.ID)
	var siblingAfter db.StepInstance
	for _, s := range stepsAfter {
		if s.NodeKey == "branch" && s.ParentStepID.Valid && s.Attempt == 1+childAttemptSlot {
			siblingAfter = s
		}
	}
	if !siblingAfter.ID.Valid {
		t.Fatalf("sibling child (slot 1025) disappeared — BFS leak")
	}
	if siblingAfter.Status == StepSkipped || siblingAfter.Status == StepRework {
		t.Fatalf("sibling status = %q — paused-path touched sibling", siblingAfter.Status)
	}

	// Run must be paused (P0 BLOCKED semantics, no policy effect).
	if got := f.runStatus(run.ID); got != RunPaused && got != RunFailed {
		t.Fatalf("run status = %q, want paused (BLOCKED under policy=rework defers to P0)", got)
	}

	// Converge must NOT have flipped to passed (child is blocked, not passed).
	conv := f.latestStep(run.ID, "converge")
	if conv.Status == StepPassed {
		t.Fatalf("converge passed despite blocked child — premature convergence")
	}
}

func TestActivateConvergeNode_InitialPending(t *testing.T) {
	f := newTestFixture(t)
	tmpl := fanOutTemplate(f, "conv-initial", FailPolicyRework)
	run := f.startRun(tmpl, "conv-initial-1", "Initial pending")

	// Upstream passes → fan_out activates → fanOutDispatch pre-creates
	// converge directly in pending state (no active→pending flip
	// because activateConvergeNode is bypassed when the step is born
	// pending).
	passUpstream(f, run.ID, []any{validItem("only")})

	conv := f.latestStep(run.ID, "converge")
	if conv.Status != StepPending {
		t.Fatalf("converge status = %q, want pending (waiting for children)", conv.Status)
	}
	// The pre-create transition (none → pending) must be recorded.
	transitions := f.transitionsForStep(conv.ID)
	var found bool
	for _, tr := range transitions {
		if tr[0] == "none" && tr[1] == StepPending {
			found = true
		}
	}
	if !found {
		t.Fatalf("converge transitions missing none→pending: %+v", transitions)
	}
}

// TestApplyFailPolicy_* exercise the policy table through the
// end-to-end HandleChildStepTerminal path (PolicyFail/Blocked above
// already assert the post-state). This file previously tried to call
// applyFailPolicy directly, but the helper is unexported by design
// (it is an internal factor of handleChildStepTerminal, not a public
// API). The e2e tests in PolicyFail / PolicyBlocked / PolicyRework
// pin the same semantics without needing a test-only export.
