package workflow

// capability_test.go — P1-7 capability routing tests. Covers the matcher
// query (ALL required keys + highest proficiency + ErrNoRows) and the
// resolveAgentForNode strategy router (capability match / fallback /
// specified / no-fallback-error).

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func upsertCap(t *testing.T, f *testFixture, agent pgtype.UUID, key string, prof int16) {
	t.Helper()
	if _, err := f.queries.UpsertAgentCapability(context.Background(), db.UpsertAgentCapabilityParams{
		AgentID: agent, CapabilityKey: key, Proficiency: prof,
	}); err != nil {
		t.Fatalf("upsert capability %s/%s: %v", util.UUIDToString(agent), key, err)
	}
}

// TestMatchAgentByCapability pins the matcher: an agent must declare ALL
// required keys (proficiency > 0); among those, highest total proficiency
// wins; no candidate → pgx.ErrNoRows.
func TestMatchAgentByCapability(t *testing.T) {
	f := newTestFixture(t)
	wsID := util.MustParseUUID(f.workspaceID)
	alice := util.MustParseUUID(f.createAgent("match-alice"))
	bob := util.MustParseUUID(f.createAgent("match-bob"))

	upsertCap(t, f, alice, "python", 80)
	upsertCap(t, f, alice, "refactor", 70)
	upsertCap(t, f, bob, "python", 90) // higher python but no refactor

	// [python, refactor]: only alice has both → alice wins despite lower python.
	got, err := f.queries.MatchAgentByCapability(context.Background(), db.MatchAgentByCapabilityParams{
		WorkspaceID: wsID, Column2: []string{"python", "refactor"},
	})
	if err != nil {
		t.Fatalf("match both: %v", err)
	}
	if got != alice {
		t.Fatalf("match both = %v, want alice (only agent with both keys)", got)
	}

	// [python] only: bob wins (proficiency 90 > 80).
	got2, err := f.queries.MatchAgentByCapability(context.Background(), db.MatchAgentByCapabilityParams{
		WorkspaceID: wsID, Column2: []string{"python"},
	})
	if err != nil {
		t.Fatalf("match python: %v", err)
	}
	if got2 != bob {
		t.Fatalf("match python = %v, want bob (higher proficiency)", got2)
	}

	// No candidate → ErrNoRows (caller falls back).
	_, err = f.queries.MatchAgentByCapability(context.Background(), db.MatchAgentByCapabilityParams{
		WorkspaceID: wsID, Column2: []string{"nonexistent-key"},
	})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("want ErrNoRows for unmatched key, got %v", err)
	}
}

// TestResolveAgentForNode_CapabilityMatch: capability strategy dispatches
// to the matched agent (not the publish-resolved fallback).
func TestResolveAgentForNode_CapabilityMatch(t *testing.T) {
	f := newTestFixture(t)
	alice := util.MustParseUUID(f.createAgent("resolve-alice"))
	bob := f.createAgent("resolve-fallback-bob")
	upsertCap(t, f, alice, "python", 80)

	run := db.WorkflowRun{WorkspaceID: util.MustParseUUID(f.workspaceID)}
	node := &SnapshotNode{NodeKey: "work", Type: NodeTypeAgent, Config: NodeConfig{
		DispatchStrategy:     DispatchStrategyCapability,
		RequiredCapabilities: []string{"python"},
		AgentID:              bob, // frozen fallback — must be overridden by the match
	}}
	got, err := f.engine.resolveAgentForNode(context.Background(), run, node)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != alice {
		t.Fatalf("resolve = %v, want matched alice (not fallback %s)", got, bob)
	}
}

// TestResolveAgentForNode_FallbackOnNoMatch: no qualifying agent → fall back
// to the frozen AgentID so dispatch never dead-ends on a missing declaration.
func TestResolveAgentForNode_FallbackOnNoMatch(t *testing.T) {
	f := newTestFixture(t)
	bob := util.MustParseUUID(f.createAgent("resolve-fb-bob"))
	run := db.WorkflowRun{WorkspaceID: util.MustParseUUID(f.workspaceID)}
	node := &SnapshotNode{NodeKey: "work", Type: NodeTypeAgent, Config: NodeConfig{
		DispatchStrategy:     DispatchStrategyCapability,
		RequiredCapabilities: []string{"nobody-has-this"},
		AgentID:              util.UUIDToString(bob),
	}}
	got, err := f.engine.resolveAgentForNode(context.Background(), run, node)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != bob {
		t.Fatalf("resolve = %v, want fallback bob", got)
	}
}

// TestResolveAgentForNode_Specified: specified strategy uses the frozen
// AgentID directly (D-7 publish resolve unchanged).
func TestResolveAgentForNode_Specified(t *testing.T) {
	f := newTestFixture(t)
	bob := util.MustParseUUID(f.createAgent("resolve-spec-bob"))
	run := db.WorkflowRun{WorkspaceID: util.MustParseUUID(f.workspaceID)}
	node := &SnapshotNode{NodeKey: "work", Type: NodeTypeAgent, Config: NodeConfig{
		DispatchStrategy: DispatchStrategySpecified,
		AgentID:          util.UUIDToString(bob),
	}}
	got, err := f.engine.resolveAgentForNode(context.Background(), run, node)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != bob {
		t.Fatalf("resolve = %v, want bob", got)
	}
}

// TestResolveAgentForNode_NoFallbackErrors: capability miss + no frozen
// AgentID → error (dispatch must never silently pick an arbitrary agent).
func TestResolveAgentForNode_NoFallbackErrors(t *testing.T) {
	f := newTestFixture(t)
	run := db.WorkflowRun{WorkspaceID: util.MustParseUUID(f.workspaceID)}
	node := &SnapshotNode{NodeKey: "work", Type: NodeTypeAgent, Config: NodeConfig{
		DispatchStrategy:     DispatchStrategyCapability,
		RequiredCapabilities: []string{"nobody-has-this"},
	}}
	if _, err := f.engine.resolveAgentForNode(context.Background(), run, node); err == nil {
		t.Fatal("resolve: want error when capability unmatched and no fallback agent_id")
	}
}
