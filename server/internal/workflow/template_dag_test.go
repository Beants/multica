package workflow

// template_dag_test.go — Wave 0 DAG validator + NextAfterAll + BFS
// DownstreamNodeKeys coverage. Pure unit tests (no DB) so they run in the
// fast unit-test lane and assert the DAG upgrade preserves P0 linear
// semantics while admitting fan_out branching.

import (
	"strings"
	"testing"
)

// fanOutNode builds a fan_out node input with the given key.
func fanOutNode(key string) NodeInput {
	return typedNode(key, NodeTypeFanOut, NodeConfig{})
}

// convergeNode builds a converge node input with the given key.
func convergeNode(key string) NodeInput {
	return typedNode(key, NodeTypeConverge, NodeConfig{})
}

// dagEdges builds an EdgeInput slice from [from, to, from, to, …] pairs.
// Priority is left at the zero default for all edges; tests that need
// explicit priority construct edges directly.
func dagEdges(pairs ...string) []EdgeInput {
	if len(pairs)%2 != 0 {
		panic("dagEdges: odd pair count")
	}
	out := make([]EdgeInput, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, EdgeInput{FromNodeKey: pairs[i], ToNodeKey: pairs[i+1]})
	}
	return out
}

// ---------------------------------------------------------------------------
// validateTemplateGraph: DAG cases (Wave 0.2)
// ---------------------------------------------------------------------------

func TestValidateGraphDAG_LinearPreserved(t *testing.T) {
	t.Parallel()
	// P0-shape linear chain must still validate under the DAG rules.
	nodes := []NodeInput{
		agentNode("a", RoleExecutor, "Agent", NodeConfig{}),
		agentNode("b", RoleExecutor, "Agent", NodeConfig{}),
		agentNode("c", RoleExecutor, "Agent", NodeConfig{}),
	}
	edges := linearEdges("a", "b", "c")
	if err := validateTemplateGraph(nodes, edges); err != nil {
		t.Fatalf("linear chain must validate under DAG: %v", err)
	}
}

func TestValidateGraphDAG_FanOutAllowed(t *testing.T) {
	t.Parallel()
	// fan_out branches into two siblings that re-converge: classic P1 shape.
	nodes := []NodeInput{
		agentNode("upstream", RoleExecutor, "Agent", NodeConfig{}),
		fanOutNode("fanout"),
		agentNode("branchA", RoleExecutor, "Agent", NodeConfig{}),
		agentNode("branchB", RoleExecutor, "Agent", NodeConfig{}),
		convergeNode("converge"),
		typedNode("end", NodeTypeEnd, NodeConfig{}),
	}
	edges := dagEdges(
		"upstream", "fanout",
		"fanout", "branchA",
		"fanout", "branchB",
		"branchA", "converge",
		"branchB", "converge",
		"converge", "end",
	)
	if err := validateTemplateGraph(nodes, edges); err != nil {
		t.Fatalf("fan_out + converge shape must validate: %v", err)
	}
}

func TestValidateGraphDAG_FanOutWithoutConvergeStillValidates(t *testing.T) {
	t.Parallel()
	// Wave 0's validator admits the SHAPE; fan_out↔converge pairing is
	// Wave 1's publish-time concern. A fan_out whose branches never
	// re-converge (each branch ends in its own end) is structurally legal.
	nodes := []NodeInput{
		fanOutNode("fanout"),
		agentNode("branchA", RoleExecutor, "Agent", NodeConfig{}),
		agentNode("branchB", RoleExecutor, "Agent", NodeConfig{}),
		typedNode("endA", NodeTypeEnd, NodeConfig{}),
		typedNode("endB", NodeTypeEnd, NodeConfig{}),
	}
	edges := dagEdges(
		"fanout", "branchA",
		"fanout", "branchB",
		"branchA", "endA",
		"branchB", "endB",
	)
	// TWO start-free structure: fanout is the only in-degree-0 node. But
	// endA/endB both terminal — this is a valid DAG shape.
	if err := validateTemplateGraph(nodes, edges); err != nil {
		t.Fatalf("fan_out without converge must still validate structurally: %v", err)
	}
}

func TestValidateGraphDAG_AgentBranchingRejected(t *testing.T) {
	t.Parallel()
	// An agent node may not branch — only fan_out can.
	nodes := []NodeInput{
		agentNode("a", RoleExecutor, "Agent", NodeConfig{}),
		agentNode("b", RoleExecutor, "Agent", NodeConfig{}),
		agentNode("c", RoleExecutor, "Agent", NodeConfig{}),
	}
	edges := []EdgeInput{
		{FromNodeKey: "a", ToNodeKey: "b"},
		{FromNodeKey: "a", ToNodeKey: "c"},
	}
	err := validateTemplateGraph(nodes, edges)
	if err == nil || !strings.Contains(err.Error(), "only fan_out may branch") {
		t.Fatalf("agent branching = %v, want 'only fan_out may branch' error", err)
	}
}

func TestValidateGraphDAG_CycleRejected(t *testing.T) {
	t.Parallel()
	// a→b→c→a: every node has inbound, so the validator detects "no start".
	// (Cycle within a reachable subgraph is caught by the DFS below; this
	// variant catches the all-cyclic case.)
	nodes := []NodeInput{
		agentNode("a", RoleExecutor, "Agent", NodeConfig{}),
		agentNode("b", RoleExecutor, "Agent", NodeConfig{}),
		agentNode("c", RoleExecutor, "Agent", NodeConfig{}),
	}
	edges := dagEdges("a", "b", "b", "c", "c", "a")
	err := validateTemplateGraph(nodes, edges)
	if err == nil ||
		(!strings.Contains(err.Error(), "cycle") && !strings.Contains(err.Error(), "no start")) {
		t.Fatalf("3-node cycle = %v, want cycle or no-start error", err)
	}
}

func TestValidateGraphDAG_ReachableCycleRejected(t *testing.T) {
	t.Parallel()
	// start→a→b→a: start has in-degree 0 (real start), but a/b form a cycle
	// reachable from start. The DFS must flag this.
	nodes := []NodeInput{
		agentNode("start", RoleExecutor, "Agent", NodeConfig{}),
		agentNode("a", RoleExecutor, "Agent", NodeConfig{}),
		agentNode("b", RoleExecutor, "Agent", NodeConfig{}),
	}
	edges := dagEdges("start", "a", "a", "b", "b", "a")
	err := validateTemplateGraph(nodes, edges)
	if err == nil || !strings.Contains(err.Error(), "cycle detected") {
		t.Fatalf("reachable cycle = %v, want 'cycle detected'", err)
	}
}

func TestValidateGraphDAG_MultiStartRejected(t *testing.T) {
	t.Parallel()
	// Two in-degree-0 nodes → "exactly one start" failure (orphan-style).
	nodes := []NodeInput{
		agentNode("a", RoleExecutor, "Agent", NodeConfig{}),
		agentNode("b", RoleExecutor, "Agent", NodeConfig{}),
	}
	err := validateTemplateGraph(nodes, nil)
	if err == nil || !strings.Contains(err.Error(), "one start node") {
		t.Fatalf("two starts = %v, want one-start error", err)
	}
}

func TestValidateGraphDAG_FanOutMustHaveOutbound(t *testing.T) {
	t.Parallel()
	// A fan_out with zero outbound is degenerate (no branching). Reject.
	nodes := []NodeInput{
		agentNode("a", RoleExecutor, "Agent", NodeConfig{}),
		fanOutNode("fanout"),
	}
	edges := dagEdges("a", "fanout") // fanout has no outbound
	err := validateTemplateGraph(nodes, edges)
	if err == nil || !strings.Contains(err.Error(), "fan_out") {
		t.Fatalf("branchless fan_out = %v, want fan_out error", err)
	}
}

// ---------------------------------------------------------------------------
// Snapshot.NextAfterAll (Wave 0.3)
// ---------------------------------------------------------------------------

func TestNextAfterAll_LinearReturnsOne(t *testing.T) {
	t.Parallel()
	snap := &Snapshot{
		Nodes: []SnapshotNode{{NodeKey: "a"}, {NodeKey: "b"}, {NodeKey: "c"}},
		Edges: []SnapshotEdge{
			{FromNodeKey: "a", ToNodeKey: "b"},
			{FromNodeKey: "b", ToNodeKey: "c"},
		},
	}
	// P0 invariant: mid-chain nodes return a 1-element slice.
	if got := snap.NextAfterAll("a"); len(got) != 1 || got[0].NodeKey != "b" {
		t.Fatalf("NextAfterAll(a) = %+v, want [b]", got)
	}
	if got := snap.NextAfterAll("b"); len(got) != 1 || got[0].NodeKey != "c" {
		t.Fatalf("NextAfterAll(b) = %+v, want [c]", got)
	}
}

func TestNextAfterAll_FanOutReturnsMany(t *testing.T) {
	t.Parallel()
	snap := &Snapshot{
		Nodes: []SnapshotNode{{NodeKey: "f"}, {NodeKey: "x"}, {NodeKey: "y"}, {NodeKey: "z"}},
		Edges: []SnapshotEdge{
			{FromNodeKey: "f", ToNodeKey: "x"},
			{FromNodeKey: "f", ToNodeKey: "y"},
			{FromNodeKey: "f", ToNodeKey: "z"},
		},
	}
	got := snap.NextAfterAll("f")
	if len(got) != 3 {
		t.Fatalf("NextAfterAll(f) = %+v, want 3 elements", got)
	}
	seen := map[string]bool{}
	for _, n := range got {
		seen[n.NodeKey] = true
	}
	if !seen["x"] || !seen["y"] || !seen["z"] {
		t.Fatalf("NextAfterAll(f) missing branches: %+v", seen)
	}
}

func TestNextAfterAll_PriorityOrder(t *testing.T) {
	t.Parallel()
	// Three edges from `root` with mixed priorities. Output must be ASC.
	snap := &Snapshot{
		Nodes: []SnapshotNode{{NodeKey: "root"}, {NodeKey: "p1"}, {NodeKey: "p2"}, {NodeKey: "p3"}},
		Edges: []SnapshotEdge{
			{FromNodeKey: "root", ToNodeKey: "p3", Priority: 30},
			{FromNodeKey: "root", ToNodeKey: "p1", Priority: 10},
			{FromNodeKey: "root", ToNodeKey: "p2", Priority: 20},
		},
	}
	got := snap.NextAfterAll("root")
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	want := []string{"p1", "p2", "p3"}
	for i, w := range want {
		if got[i].NodeKey != w {
			t.Fatalf("NextAfterAll order[%d] = %q, want %q (full: %+v)", i, got[i].NodeKey, w, got)
		}
	}
}

func TestNextAfterAll_PriorityTieBrokenByToKey(t *testing.T) {
	t.Parallel()
	// Two edges with equal priority — tie broken alphabetically by ToNodeKey.
	snap := &Snapshot{
		Nodes: []SnapshotNode{{NodeKey: "root"}, {NodeKey: "z"}, {NodeKey: "a"}, {NodeKey: "m"}},
		Edges: []SnapshotEdge{
			{FromNodeKey: "root", ToNodeKey: "z", Priority: 5},
			{FromNodeKey: "root", ToNodeKey: "a", Priority: 5},
			{FromNodeKey: "root", ToNodeKey: "m", Priority: 5},
		},
	}
	got := snap.NextAfterAll("root")
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	want := []string{"a", "m", "z"}
	for i, w := range want {
		if got[i].NodeKey != w {
			t.Fatalf("tie-break order[%d] = %q, want %q", i, got[i].NodeKey, w)
		}
	}
}

func TestNextAfterAll_TailReturnsEmpty(t *testing.T) {
	t.Parallel()
	snap := &Snapshot{
		Nodes: []SnapshotNode{{NodeKey: "a"}, {NodeKey: "b"}},
		Edges: []SnapshotEdge{{FromNodeKey: "a", ToNodeKey: "b"}},
	}
	if got := snap.NextAfterAll("b"); len(got) != 0 {
		t.Fatalf("NextAfterAll(b) = %+v, want empty (tail)", got)
	}
	if got := snap.NextAfterAll("unknown"); len(got) != 0 {
		t.Fatalf("NextAfterAll(unknown) = %+v, want empty", got)
	}
}

// ---------------------------------------------------------------------------
// Snapshot.DownstreamNodeKeys (Wave 0.4 BFS)
// ---------------------------------------------------------------------------

func TestDownstreamBFS_LinearMatchesP0(t *testing.T) {
	t.Parallel()
	snap := &Snapshot{
		Nodes: []SnapshotNode{{NodeKey: "a"}, {NodeKey: "b"}, {NodeKey: "c"}, {NodeKey: "d"}},
		Edges: []SnapshotEdge{
			{FromNodeKey: "a", ToNodeKey: "b"},
			{FromNodeKey: "b", ToNodeKey: "c"},
			{FromNodeKey: "c", ToNodeKey: "d"},
		},
	}
	// P0 chain walk was [b, c, d]; BFS over a linear graph returns the
	// same set in chain order (BFS happens to match DFS for linear).
	got := snap.DownstreamNodeKeys("a")
	want := []string{"b", "c", "d"}
	if len(got) != len(want) {
		t.Fatalf("DownstreamNodeKeys(a) = %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("DownstreamNodeKeys(a)[%d] = %q, want %q (full: %v)", i, got[i], w, got)
		}
	}
	// Downstream of mid-chain node excludes upstream.
	got = snap.DownstreamNodeKeys("b")
	want = []string{"c", "d"}
	if len(got) != len(want) {
		t.Fatalf("DownstreamNodeKeys(b) = %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("DownstreamNodeKeys(b)[%d] = %q, want %q", i, got[i], w)
		}
	}
}

func TestDownstreamBFS_FanOutExpands(t *testing.T) {
	t.Parallel()
	// upstream → fanout → [branchA, branchB] → converge → end
	snap := &Snapshot{
		Nodes: []SnapshotNode{
			{NodeKey: "upstream"},
			{NodeKey: "fanout"},
			{NodeKey: "branchA"},
			{NodeKey: "branchB"},
			{NodeKey: "converge"},
			{NodeKey: "end"},
		},
		Edges: []SnapshotEdge{
			{FromNodeKey: "upstream", ToNodeKey: "fanout"},
			{FromNodeKey: "fanout", ToNodeKey: "branchA"},
			{FromNodeKey: "fanout", ToNodeKey: "branchB"},
			{FromNodeKey: "branchA", ToNodeKey: "converge"},
			{FromNodeKey: "branchB", ToNodeKey: "converge"},
			{FromNodeKey: "converge", ToNodeKey: "end"},
		},
	}
	// Downstream of fanout must include branches + converge + end (4 nodes).
	got := snap.DownstreamNodeKeys("fanout")
	if len(got) != 4 {
		t.Fatalf("DownstreamNodeKeys(fanout) = %v, want 4 nodes", got)
	}
	seen := map[string]bool{}
	for _, k := range got {
		seen[k] = true
	}
	for _, want := range []string{"branchA", "branchB", "converge", "end"} {
		if !seen[want] {
			t.Fatalf("DownstreamNodeKeys(fanout) missing %q (got %v)", want, got)
		}
	}
	// Downstream of upstream adds fanout itself.
	got = snap.DownstreamNodeKeys("upstream")
	if len(got) != 5 {
		t.Fatalf("DownstreamNodeKeys(upstream) = %v, want 5 nodes", got)
	}
}

func TestDownstreamBFS_CycleSafe(t *testing.T) {
	t.Parallel()
	// a→b→a (cycle). BFS must terminate (visited guard) and not loop forever.
	// Synchronous call: if BFS loops, the test binary hangs and the test
	// runner kills it (go test default 10m timeout). No goroutine indirection
	// needed.
	loopy := &Snapshot{
		Nodes: []SnapshotNode{{NodeKey: "a"}, {NodeKey: "b"}},
		Edges: []SnapshotEdge{
			{FromNodeKey: "a", ToNodeKey: "b"},
			{FromNodeKey: "b", ToNodeKey: "a"},
		},
	}
	got := loopy.DownstreamNodeKeys("a")
	// a→b, b→a (visited). Result: just [b].
	if len(got) != 1 || got[0] != "b" {
		t.Fatalf("DownstreamNodeKeys(a) on cycle = %v, want [b]", got)
	}
}

// ---------------------------------------------------------------------------
// P0 seed linear equivalence (Wave 0.6 white-box assertion)
// ---------------------------------------------------------------------------

func TestDAGLinearEquivalence_StandardSeed(t *testing.T) {
	t.Parallel()
	// Mirror the standard seed's 9-node chain (seed.go: standardSeedDef).
	chain := []string{"plan", "plan-gate", "spec-freeze", "implement",
		"baseline-gate", "api-gate", "review", "final-acceptance", "done"}
	snap := linearSnapshot(t, chain)

	// Every mid-chain node returns a 1-element slice; the tail returns 0.
	for i, key := range chain {
		got := snap.NextAfterAll(key)
		if i < len(chain)-1 {
			if len(got) != 1 || got[0].NodeKey != chain[i+1] {
				t.Fatalf("NextAfterAll(%q) = %+v, want [%q]", key, got, chain[i+1])
			}
		} else {
			if len(got) != 0 {
				t.Fatalf("NextAfterAll(tail %q) = %+v, want empty", key, got)
			}
		}
	}

	// DownstreamNodeKeys BFS returns every later node in chain order.
	got := snap.DownstreamNodeKeys(chain[0])
	if len(got) != len(chain)-1 {
		t.Fatalf("DownstreamNodeKeys(%q) = %v, want %d nodes", chain[0], got, len(chain)-1)
	}
	for i, want := range chain[1:] {
		if got[i] != want {
			t.Fatalf("DownstreamNodeKeys(%q)[%d] = %q, want %q", chain[0], i, got[i], want)
		}
	}
}

func TestDAGLinearEquivalence_BugfixSeed(t *testing.T) {
	t.Parallel()
	// Mirror the bugfix seed's 6-node chain (seed.go: bugfixSeedDef).
	chain := []string{"plan-lite", "implement", "baseline-gate", "review",
		"final-acceptance", "done"}
	snap := linearSnapshot(t, chain)

	for i, key := range chain {
		got := snap.NextAfterAll(key)
		if i < len(chain)-1 {
			if len(got) != 1 || got[0].NodeKey != chain[i+1] {
				t.Fatalf("NextAfterAll(%q) = %+v, want [%q]", key, got, chain[i+1])
			}
		} else {
			if len(got) != 0 {
				t.Fatalf("NextAfterAll(tail %q) = %+v, want empty", key, got)
			}
		}
	}

	got := snap.DownstreamNodeKeys(chain[0])
	if len(got) != len(chain)-1 {
		t.Fatalf("DownstreamNodeKeys(%q) = %v, want %d nodes", chain[0], got, len(chain)-1)
	}
	for i, want := range chain[1:] {
		if got[i] != want {
			t.Fatalf("DownstreamNodeKeys(%q)[%d] = %q, want %q", chain[0], i, got[i], want)
		}
	}
}

// linearSnapshot builds a Snapshot of pure agent nodes connected linearly
// by priority-1 edges. The node types don't affect NextAfterAll / BFS
// behavior (those are graph-structure-only), so agent is fine for all.
func linearSnapshot(t *testing.T, chain []string) *Snapshot {
	t.Helper()
	nodes := make([]SnapshotNode, len(chain))
	for i, k := range chain {
		nodes[i] = SnapshotNode{NodeKey: k}
	}
	edges := make([]SnapshotEdge, len(chain)-1)
	for i := 0; i+1 < len(chain); i++ {
		edges[i] = SnapshotEdge{FromNodeKey: chain[i], ToNodeKey: chain[i+1], Priority: 1}
	}
	return &Snapshot{Nodes: nodes, Edges: edges}
}

// ---------------------------------------------------------------------------
// Engine lookaheadTargets helper (Wave 0.5)
// ---------------------------------------------------------------------------

func TestLookaheadTargets_ByType(t *testing.T) {
	t.Parallel()
	snap := &Snapshot{
		Nodes: []SnapshotNode{
			{NodeKey: "fanout", Type: NodeTypeFanOut},
			{NodeKey: "agent", Type: NodeTypeAgent},
			{NodeKey: "conv", Type: NodeTypeConverge},
			{NodeKey: "end", Type: NodeTypeEnd},
			{NodeKey: "c1"},
			{NodeKey: "c2"},
			{NodeKey: "after"},
		},
		Edges: []SnapshotEdge{
			{FromNodeKey: "fanout", ToNodeKey: "c1", Priority: 1},
			{FromNodeKey: "fanout", ToNodeKey: "c2", Priority: 2},
			{FromNodeKey: "agent", ToNodeKey: "after", Priority: 1},
			{FromNodeKey: "conv", ToNodeKey: "after", Priority: 1},
			{FromNodeKey: "end", ToNodeKey: "after", Priority: 1},
		},
	}
	// fan_out: no lookahead (children are dynamic, expanded by Wave 2).
	fanOutNode := snap.NodeByKey("fanout")
	if got := lookaheadTargets(snap, fanOutNode); len(got) != 0 {
		t.Fatalf("lookaheadTargets(fan_out) = %+v, want empty", got)
	}
	// nil safety.
	if got := lookaheadTargets(snap, nil); len(got) != 0 {
		t.Fatalf("lookaheadTargets(nil) = %+v, want empty", got)
	}
	// agent / converge / end: lookahead returns the single downstream.
	for _, key := range []string{"agent", "conv", "end"} {
		n := snap.NodeByKey(key)
		got := lookaheadTargets(snap, n)
		if len(got) != 1 || got[0].NodeKey != "after" {
			t.Fatalf("lookaheadTargets(%s) = %+v, want [after]", key, got)
		}
	}
}
