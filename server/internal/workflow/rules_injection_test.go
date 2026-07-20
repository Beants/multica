package workflow

// rules_injection_test.go — P1-4 soft context_inject handoff injection.

import (
	"context"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// TestBuildHandoffNote_SoftRuleInjection: a soft context_inject rule bound
// to the dispatching agent shows up under [team rules] in the handoff.
func TestBuildHandoffNote_SoftRuleInjection(t *testing.T) {
	f := newTestFixture(t)
	wsID := util.MustParseUUID(f.workspaceID)
	agentID := util.MustParseUUID(f.createAgent("rules-agent"))

	rule, err := f.queries.CreateWorkflowRule(context.Background(), db.CreateWorkflowRuleParams{
		WorkspaceID: wsID, Name: "test-coverage", Level: "soft", Scope: "agent",
		Content: "PR description must include test coverage notes", Config: []byte("{}"),
	})
	if err != nil {
		t.Fatalf("create rule: %v", err)
	}
	if _, err := f.queries.CreateWorkflowRuleBinding(context.Background(), db.CreateWorkflowRuleBindingParams{
		RuleID: rule.ID, TargetType: "agent", TargetID: agentID, Column4: "context_inject",
	}); err != nil {
		t.Fatalf("create binding: %v", err)
	}

	node := &SnapshotNode{NodeKey: "work", Type: NodeTypeAgent, Name: "work", Config: NodeConfig{Instructions: "ship it"}}
	snap := &Snapshot{Nodes: []SnapshotNode{*node}}
	run := db.WorkflowRun{WorkspaceID: wsID}
	step := db.StepInstance{Attempt: 1}

	note := f.engine.buildHandoffNote(context.Background(), run, snap, node, step, nil, false, agentID)
	if !strings.Contains(note, "test coverage notes") {
		t.Fatalf("handoff missing soft rule content; got:\n%s", note)
	}
	if !strings.Contains(note, "[team rules]") {
		t.Fatalf("handoff missing [team rules] section; got:\n%s", note)
	}
}

// TestBuildHandoffNote_NoSoftRulesOmitsSection: no bound soft rules → the
// [team rules] section is absent (no empty header).
func TestBuildHandoffNote_NoSoftRulesOmitsSection(t *testing.T) {
	f := newTestFixture(t)
	agentID := util.MustParseUUID(f.createAgent("no-rules-agent"))
	node := &SnapshotNode{NodeKey: "work", Type: NodeTypeAgent, Name: "work", Config: NodeConfig{Instructions: "ship it"}}
	snap := &Snapshot{Nodes: []SnapshotNode{*node}}
	run := db.WorkflowRun{WorkspaceID: util.MustParseUUID(f.workspaceID)}
	step := db.StepInstance{Attempt: 1}

	note := f.engine.buildHandoffNote(context.Background(), run, snap, node, step, nil, false, agentID)
	if strings.Contains(note, "[team rules]") {
		t.Fatalf("note should omit [team rules] when none bound; got:\n%s", note)
	}
}

// TestBuildHandoffNote_HardRuleNotInjected: a hard-level rule bound to the
// agent is NOT injected into context (hard rules gate_check via gate_type=
// rules in P1-4b; context_inject applies only to soft).
func TestBuildHandoffNote_HardRuleNotInjected(t *testing.T) {
	f := newTestFixture(t)
	wsID := util.MustParseUUID(f.workspaceID)
	agentID := util.MustParseUUID(f.createAgent("hard-rule-agent"))
	rule, err := f.queries.CreateWorkflowRule(context.Background(), db.CreateWorkflowRuleParams{
		WorkspaceID: wsID, Name: "hard-red-line", Level: "hard", Scope: "agent",
		Content: "never commit secrets plaintext", Config: []byte("{}"),
	})
	if err != nil {
		t.Fatalf("create rule: %v", err)
	}
	if _, err := f.queries.CreateWorkflowRuleBinding(context.Background(), db.CreateWorkflowRuleBindingParams{
		RuleID: rule.ID, TargetType: "agent", TargetID: agentID, Column4: "context_inject",
	}); err != nil {
		t.Fatalf("create binding: %v", err)
	}
	node := &SnapshotNode{NodeKey: "work", Type: NodeTypeAgent, Name: "work", Config: NodeConfig{Instructions: "x"}}
	snap := &Snapshot{Nodes: []SnapshotNode{*node}}
	run := db.WorkflowRun{WorkspaceID: wsID}
	step := db.StepInstance{Attempt: 1}

	note := f.engine.buildHandoffNote(context.Background(), run, snap, node, step, nil, false, agentID)
	if strings.Contains(note, "never commit secrets") {
		t.Fatalf("hard rule must NOT be context-injected (only soft); got:\n%s", note)
	}
}
