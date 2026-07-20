package workflow

// complex_template_test.go — P1-9 integration: proves the P1 capability
// suite composes end-to-end instead of each passing in isolation. The
// template wires capability routing (P1-7) on the planner into fan_out
// fan-out + converge AND-join (P1-1), exercising exit standard #1 (fan_out
// splits ≥3 parallel children that converge on completion).

import (
	"context"
	"testing"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// TestComplexTemplate_CapabilityPlanFanOutConverge wires every P1 capability
// into one run: a capability-routed planner emits a subtask list, fan_out
// expands it into 3 parallel children, converge AND-joins them. This is the
// smallest template that would break if any of P1-1/P1-7 regressed in
// combination (each passes alone in its own package test).
func TestComplexTemplate_CapabilityPlanFanOutConverge(t *testing.T) {
	f := newTestFixture(t)

	// P1-7: planner declares the "planning" capability the plan node requires.
	planner := util.MustParseUUID(f.createAgent("cx-planner"))
	if _, err := f.queries.UpsertAgentCapability(context.Background(), db.UpsertAgentCapabilityParams{
		AgentID: planner, CapabilityKey: "planning", Proficiency: 80,
	}); err != nil {
		t.Fatalf("seed planner capability: %v", err)
	}

	// Template: plan(capability) → fan_out → branch → converge → end.
	// plan uses DispatchStrategyCapability so it routes to the planner agent
	// at activation, NOT the publish-resolved fallback ("Executor Agent").
	// Its exit_fields carry the subtask list fan_out consumes via items_field.
	plan := agentNode("plan", RoleExecutor, "Executor Agent", NodeConfig{
		DispatchStrategy:     DispatchStrategyCapability,
		RequiredCapabilities: []string{"planning"},
		Instructions:         "Plan the change set and emit subtasks",
		ExitFields: &ExitFieldsSchema{Fields: []ExitFieldSpec{
			{Name: "subtasks", Type: "array", Required: true},
		}},
	})
	fanOut := typedNode("fanout", NodeTypeFanOut, NodeConfig{
		ItemsField: "subtasks", FailPolicy: FailPolicyRework,
	})
	branch := agentNode("branch", RoleExecutor, "Executor Agent", NodeConfig{
		Instructions: "Execute one child change task",
	})
	converge := typedNode("converge", NodeTypeConverge, NodeConfig{})
	end := typedNode("end", NodeTypeEnd, NodeConfig{})
	tmpl := f.createPublishedTemplate("complex-cap-fanout", []NodeInput{
		plan, fanOut, branch, converge, end,
	}, dagEdges(
		"plan", "fanout",
		"fanout", "branch",
		"branch", "converge",
		"converge", "end",
	))

	run := f.startRun(tmpl, "complex-1", "Complex")

	// AC2: plan dispatched to the capability-matched planner, not the
	// publish-resolved fallback ("Executor Agent").
	planStep := f.latestStep(run.ID, "plan")
	if util.UUIDToString(planStep.AgentID) != util.UUIDToString(planner) {
		t.Fatalf("plan dispatched to %v, want capability-matched planner %s", planStep.AgentID, planner)
	}

	// plan emits three subtasks → fan_out expands (AC1/AC3).
	f.passExecutorStep(run.ID, "plan", map[string]any{
		"subtasks": []any{
			validItem("config-change"),
			validItem("template-change"),
			validItem("code-change"),
		},
	})

	fanOutStep := f.latestStep(run.ID, "fanout")
	if fanOutStep.Status != StepPassed {
		t.Fatalf("fan_out status = %q, want passed (expanded + transitioned)", fanOutStep.Status)
	}

	// AC3: three child branch steps, each parented to the fan_out step.
	steps, err := f.queries.ListStepInstancesForRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("list steps: %v", err)
	}
	children := 0
	for _, s := range steps {
		if s.NodeKey == "branch" && s.ParentStepID.Valid {
			children++
		}
	}
	if children != 3 {
		t.Fatalf("expected 3 branch children parented to fan_out, got %d", children)
	}

	// AC4: converge is pending, AND-joining on the children reaching terminal.
	convStep := f.latestStep(run.ID, "converge")
	if convStep.Status != StepPending {
		t.Fatalf("converge status = %q, want pending (AND-join waiting for children)", convStep.Status)
	}
}
