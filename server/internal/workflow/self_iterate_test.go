package workflow

// self_iterate_test.go — P2-8 platform self-iteration scenario e2e (出口标准
// #4): the platform eats its own dogfood — analyze → implement → verify(gate)
// → acceptance → end. Pins the self-iterate loop composes with a gate (verify)
// between implement and human acceptance.

import (
	"testing"
)

func TestSelfIterateScenario_AnalyzeImplementVerifyAcceptance(t *testing.T) {
	f := newTestFixture(t)
	analyze := agentNode("analyze", RoleExecutor, "Executor Agent", NodeConfig{
		Instructions: "Analyze the platform improvement",
	})
	implement := agentNode("implement", RoleExecutor, "Executor Agent", NodeConfig{
		Instructions: "Implement the change",
	})
	// verify gate: inline script that always passes (MVP — a real self-iterate
	// verify runs the platform's own test suite).
	verify := typedNode("verify", NodeTypeGate, NodeConfig{
		GateType:         GateTypeScript,
		GateInlineScript: `echo '{"status":"pass"}'`,
	})
	acceptance := typedNode("acceptance", NodeTypeAcceptance, NodeConfig{})
	end := typedNode("end", NodeTypeEnd, NodeConfig{})
	tmpl := f.createPublishedTemplate("self-iterate", []NodeInput{
		analyze, implement, verify, acceptance, end,
	}, dagEdges(
		"analyze", "implement",
		"implement", "verify",
		"verify", "acceptance",
		"acceptance", "end",
	))

	run := f.startRun(tmpl, "self-iterate-1", "Self-iterate")

	// analyze dispatches.
	if !f.latestStep(run.ID, "analyze").AgentTaskID.Valid {
		t.Fatalf("analyze step has no task")
	}

	// pass analyze → implement active.
	f.passExecutorStep(run.ID, "analyze", nil)
	if f.latestStep(run.ID, "implement").Status != StepActive {
		t.Fatalf("implement not active after analyze passed")
	}

	// pass implement → verify gate runs (inline pass) → acceptance pending.
	f.passExecutorStep(run.ID, "implement", nil)
	verifyStep := f.latestStep(run.ID, "verify")
	if verifyStep.Status != StepPassed {
		t.Fatalf("verify gate status = %q, want passed (inline pass script)", verifyStep.Status)
	}
	if f.latestStep(run.ID, "acceptance").Status != StepActive {
		t.Fatalf("acceptance not active after verify passed (should be awaiting human verdict)")
	}
}
