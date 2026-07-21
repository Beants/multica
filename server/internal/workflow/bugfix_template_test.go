package workflow

// bugfix_template_test.go — P2-7 Bug Fix scenario e2e (出口标准 #3): the
// short loop plan → implement → acceptance → end drives end-to-end. The full
// bugfix seed (plan-lite/implement/baseline-gate/review/final-acceptance) is
// a longer linear chain already covered by seed_test; this pins the
// scenario's core shape composes (agent → agent → acceptance → end) the way
// a real bug-fix run would flow.

import (
	"testing"
)

// TestBugFixScenario_PlanImplementAcceptance drives the bugfix short loop:
// plan dispatches → plan passes → implement activates (acceptance gates next).
func TestBugFixScenario_PlanImplementAcceptance(t *testing.T) {
	f := newTestFixture(t)
	plan := agentNode("plan", RoleExecutor, "Executor Agent", NodeConfig{
		Instructions: "Plan the bug fix (root cause + minimal change)",
	})
	implement := agentNode("implement", RoleExecutor, "Executor Agent", NodeConfig{
		Instructions: "Implement the fix + tests",
	})
	acceptance := typedNode("acceptance", NodeTypeAcceptance, NodeConfig{})
	end := typedNode("end", NodeTypeEnd, NodeConfig{})
	tmpl := f.createPublishedTemplate("bugfix-scenario", []NodeInput{
		plan, implement, acceptance, end,
	}, dagEdges(
		"plan", "implement",
		"implement", "acceptance",
		"acceptance", "end",
	))

	run := f.startRun(tmpl, "bugfix-1", "BugFix scenario")

	// plan dispatched (has a task).
	planStep := f.latestStep(run.ID, "plan")
	if !planStep.AgentTaskID.Valid {
		t.Fatalf("plan step has no task; bugfix run did not dispatch")
	}

	// pass plan → implement activates.
	f.passExecutorStep(run.ID, "plan", nil)
	implStep := f.latestStep(run.ID, "implement")
	if implStep.Status != StepActive {
		t.Fatalf("implement status = %q, want active (plan passed → next node)", implStep.Status)
	}

	// acceptance is pending (gating implement's verdict).
	accStep := f.latestStep(run.ID, "acceptance")
	if accStep.Status != StepPending {
		t.Fatalf("acceptance status = %q, want pending (waiting for implement)", accStep.Status)
	}
}
