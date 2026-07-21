package workflow

// backlog_triage_test.go — P2-9 historical backlog pool scenario e2e (出口标准
// #5): autopilot-pulled backlog issues flow through triage → review → assign.
// MVP pins the triage loop composes; the autopilot pull trigger itself is a
// follow-up (this test seeds the run directly, as P2-7/8 do).

import (
	"testing"
)

func TestBacklogTriageScenario_TriageReviewAssign(t *testing.T) {
	f := newTestFixture(t)
	triage := agentNode("triage", RoleExecutor, "Executor Agent", NodeConfig{
		Instructions: "Triage the backlog issue (dup? priority? owner?)",
	})
	review := typedNode("review", NodeTypeAcceptance, NodeConfig{})
	end := typedNode("end", NodeTypeEnd, NodeConfig{})
	tmpl := f.createPublishedTemplate("backlog-triage", []NodeInput{
		triage, review, end,
	}, dagEdges(
		"triage", "review",
		"review", "end",
	))

	run := f.startRun(tmpl, "backlog-1", "Backlog triage")

	// triage dispatches.
	if !f.latestStep(run.ID, "triage").AgentTaskID.Valid {
		t.Fatalf("triage step has no task")
	}

	// pass triage → review (acceptance) active, awaiting human.
	f.passExecutorStep(run.ID, "triage", nil)
	if f.latestStep(run.ID, "review").Status != StepActive {
		t.Fatalf("review not active after triage passed (should await human verdict)")
	}
}
