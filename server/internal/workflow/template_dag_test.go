package workflow

// template_dag_test.go — Wave 0 DAG validator + NextAfterAll + BFS
// DownstreamNodeKeys coverage. Pure unit tests (no DB) so they run in the
// fast unit-test lane and assert the DAG upgrade preserves P0 linear
// semantics while admitting fan_out branching.

import (
	"encoding/json"
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
	// Wave 1 tightens the rules — fan_out must declare items_field and the
	// upstream must declare the matching array exit_field — so this fixture
	// carries a complete, publishable fan_out subgraph.
	upstream := agentNode("upstream", RoleExecutor, "Agent", NodeConfig{
		ExitFields: &ExitFieldsSchema{Fields: []ExitFieldSpec{{Name: "subtasks", Type: "array"}}},
	})
	fanOut := typedNode("fanout", NodeTypeFanOut, NodeConfig{ItemsField: "subtasks"})
	nodes := []NodeInput{
		upstream,
		fanOut,
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

func TestValidateGraphDAG_FanOutWithoutConvergeRejected(t *testing.T) {
	t.Parallel()
	// Wave 0 admitted this shape structurally; Wave 1's pairing rule
	// (ValidateConvergePairing) rejects any fan_out whose branches never
	// reach a converge node. The items_field on fan_out + upstream schema
	// are valid so the failure isolates the pairing rule.
	upstream := agentNode("upstream", RoleExecutor, "Agent", NodeConfig{
		ExitFields: &ExitFieldsSchema{Fields: []ExitFieldSpec{{Name: "subtasks", Type: "array"}}},
	})
	fanOut := typedNode("fanout", NodeTypeFanOut, NodeConfig{ItemsField: "subtasks"})
	nodes := []NodeInput{
		upstream,
		fanOut,
		agentNode("branchA", RoleExecutor, "Agent", NodeConfig{}),
		agentNode("branchB", RoleExecutor, "Agent", NodeConfig{}),
		typedNode("endA", NodeTypeEnd, NodeConfig{}),
		typedNode("endB", NodeTypeEnd, NodeConfig{}),
	}
	edges := dagEdges(
		"upstream", "fanout",
		"fanout", "branchA",
		"fanout", "branchB",
		"branchA", "endA",
		"branchB", "endB",
	)
	err := validateTemplateGraph(nodes, edges)
	if err == nil || !strings.Contains(err.Error(), "no downstream path to a converge") {
		t.Fatalf("branchless fan_out = %v, want pairing error", err)
	}
}

// TestValidateGraphDAG_AgentBranchingAllowed_P1_2 (P1-2): agent nodes MAY
// branch via conditional routing. Multi catch-all (two condition=nil edges
// on the same agent) is the AC10 violation; distinct conditions are fine.
func TestValidateGraphDAG_AgentBranchingAllowed_P1_2(t *testing.T) {
	t.Parallel()
	// Two condition=nil edges on agent "a" → AC10 ambiguous catch-all.
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
	if err == nil || !strings.Contains(err.Error(), "catch-all") {
		t.Fatalf("agent multi catch-all = %v, want AC10 rejection", err)
	}

	// One conditional + one catch-all on agent "a" → P1-2 legal routing.
	cond := json.RawMessage(`{"==":[{"var":"verdict.result"},"pass"]}`)
	nodes2 := []NodeInput{
		agentNode("a", RoleExecutor, "Agent", NodeConfig{}),
		agentNode("b", RoleExecutor, "Agent", NodeConfig{}),
		agentNode("c", RoleExecutor, "Agent", NodeConfig{}),
	}
	edges2 := []EdgeInput{
		{FromNodeKey: "a", ToNodeKey: "b", Priority: 1, Condition: cond},
		{FromNodeKey: "a", ToNodeKey: "c", Priority: 2}, // catch-all
	}
	if err := validateTemplateGraph(nodes2, edges2); err != nil {
		t.Fatalf("agent conditional + catch-all must PASS under P1-2: %v", err)
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
// Snapshot.NextAfterAll (Wave 0.3 + P1-2 condition routing)
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
	// P1-2: nil evalCtx = topology mode (all edges hit; ≤1 element return).
	if got := snap.NextAfterAll("a", nil); len(got) != 1 || got[0].NodeKey != "b" {
		t.Fatalf("NextAfterAll(a) = %+v, want [b]", got)
	}
	if got := snap.NextAfterAll("b", nil); len(got) != 1 || got[0].NodeKey != "c" {
		t.Fatalf("NextAfterAll(b) = %+v, want [c]", got)
	}
}

// TestNextAfterAll_TopologyModeReturnsFirstByPriority (P1-2): nil evalCtx is
// topology mode. A multi-edge node returns ≤1 element — the highest-priority
// candidate. This replaces the P0/Wave-0 behavior where NextAfterAll returned
// N elements (the slice-based signalAction loop now sees 0/1 elements; future
// P1-9 fan_out multi-activation will add a separate NextAfterAllMatched API).
func TestNextAfterAll_TopologyModeReturnsFirstByPriority(t *testing.T) {
	t.Parallel()
	snap := &Snapshot{
		Nodes: []SnapshotNode{{NodeKey: "f"}, {NodeKey: "x"}, {NodeKey: "y"}, {NodeKey: "z"}},
		Edges: []SnapshotEdge{
			{FromNodeKey: "f", ToNodeKey: "x", Priority: 30},
			{FromNodeKey: "f", ToNodeKey: "y", Priority: 10},
			{FromNodeKey: "f", ToNodeKey: "z", Priority: 20},
		},
	}
	got := snap.NextAfterAll("f", nil)
	if len(got) != 1 || got[0].NodeKey != "y" {
		t.Fatalf("NextAfterAll(f, nil) = %+v, want [y] (priority 10 wins)", got)
	}
}

func TestNextAfterAll_PriorityTieBrokenByToKey(t *testing.T) {
	t.Parallel()
	// Equal-priority edges — tie broken alphabetically by ToNodeKey. The
	// topology-mode return is the single first element ("a").
	snap := &Snapshot{
		Nodes: []SnapshotNode{{NodeKey: "root"}, {NodeKey: "z"}, {NodeKey: "a"}, {NodeKey: "m"}},
		Edges: []SnapshotEdge{
			{FromNodeKey: "root", ToNodeKey: "z", Priority: 5},
			{FromNodeKey: "root", ToNodeKey: "a", Priority: 5},
			{FromNodeKey: "root", ToNodeKey: "m", Priority: 5},
		},
	}
	got := snap.NextAfterAll("root", nil)
	if len(got) != 1 || got[0].NodeKey != "a" {
		t.Fatalf("NextAfterAll(root, nil) = %+v, want [a] (alphabetical tiebreak)", got)
	}
}

func TestNextAfterAll_TailReturnsEmpty(t *testing.T) {
	t.Parallel()
	snap := &Snapshot{
		Nodes: []SnapshotNode{{NodeKey: "a"}, {NodeKey: "b"}},
		Edges: []SnapshotEdge{{FromNodeKey: "a", ToNodeKey: "b"}},
	}
	if got := snap.NextAfterAll("b", nil); len(got) != 0 {
		t.Fatalf("NextAfterAll(b) = %+v, want empty (tail)", got)
	}
	if got := snap.NextAfterAll("unknown", nil); len(got) != 0 {
		t.Fatalf("NextAfterAll(unknown) = %+v, want empty", got)
	}
}

// ---------------------------------------------------------------------------
// Snapshot.NextAfterAll P1-2 condition routing (Wave 2)
// ---------------------------------------------------------------------------

// jsonRaw is a tiny test helper for inline JSON condition literals.
func jsonRaw(t *testing.T, expr string) json.RawMessage {
	t.Helper()
	return json.RawMessage(expr)
}

// TestNextAfterAll_ConditionMatchedBranch (PRD AC5): verdict=pass routes to
// branch A, verdict=fail routes to branch B. Two conditional edges with
// distinct priorities — the matched one wins regardless of priority.
func TestNextAfterAll_ConditionMatchedBranch(t *testing.T) {
	t.Parallel()
	passExpr := jsonRaw(t, `{"==":[{"var":"verdict.result"},"pass"]}`)
	failExpr := jsonRaw(t, `{"==":[{"var":"verdict.result"},"fail"]}`)
	snap := &Snapshot{
		Nodes: []SnapshotNode{{NodeKey: "decide"}, {NodeKey: "onPass"}, {NodeKey: "onFail"}},
		Edges: []SnapshotEdge{
			{FromNodeKey: "decide", ToNodeKey: "onPass", Priority: 1, Condition: passExpr},
			{FromNodeKey: "decide", ToNodeKey: "onFail", Priority: 2, Condition: failExpr},
		},
	}
	passCtx := map[string]any{"verdict": map[string]any{"result": "pass"}}
	got := snap.NextAfterAll("decide", passCtx)
	if len(got) != 1 || got[0].NodeKey != "onPass" {
		t.Fatalf("verdict=pass routes to %+v, want [onPass]", got)
	}
	failCtx := map[string]any{"verdict": map[string]any{"result": "fail"}}
	got = snap.NextAfterAll("decide", failCtx)
	if len(got) != 1 || got[0].NodeKey != "onFail" {
		t.Fatalf("verdict=fail routes to %+v, want [onFail]", got)
	}
}

// TestNextAfterAll_ConditionPriorityOrder (PRD Q5): when multiple conditions
// match, the highest-priority (lowest number) wins.
func TestNextAfterAll_ConditionPriorityOrder(t *testing.T) {
	t.Parallel()
	truthy := jsonRaw(t, `{"==":[1,1]}`)
	snap := &Snapshot{
		Nodes: []SnapshotNode{{NodeKey: "root"}, {NodeKey: "p1"}, {NodeKey: "p2"}},
		Edges: []SnapshotEdge{
			{FromNodeKey: "root", ToNodeKey: "p2", Priority: 2, Condition: truthy},
			{FromNodeKey: "root", ToNodeKey: "p1", Priority: 1, Condition: truthy},
		},
	}
	got := snap.NextAfterAll("root", map[string]any{})
	if len(got) != 1 || got[0].NodeKey != "p1" {
		t.Fatalf("both conditions match → priority 1 wins; got %+v, want [p1]", got)
	}
}

// TestNextAfterAll_CatchAllFallback (PRD AC6): when no condition matches, the
// condition=nil catch-all edge is selected.
func TestNextAfterAll_CatchAllFallback(t *testing.T) {
	t.Parallel()
	never := jsonRaw(t, `{"==":[1,2]}`)
	snap := &Snapshot{
		Nodes: []SnapshotNode{{NodeKey: "decide"}, {NodeKey: "branchA"}, {NodeKey: "default"}},
		Edges: []SnapshotEdge{
			{FromNodeKey: "decide", ToNodeKey: "branchA", Priority: 1, Condition: never},
			{FromNodeKey: "decide", ToNodeKey: "default", Priority: 2}, // catch-all
		},
	}
	got := snap.NextAfterAll("decide", map[string]any{})
	if len(got) != 1 || got[0].NodeKey != "default" {
		t.Fatalf("no condition matched → catch-all default; got %+v, want [default]", got)
	}
}

// TestNextAfterAll_NoMatchNoCatchAll (PRD AC7): when no condition matches and
// there is no catch-all, NextAfterAll returns an empty slice (callsite treats
// this as run blocked).
func TestNextAfterAll_NoMatchNoCatchAll(t *testing.T) {
	t.Parallel()
	never := jsonRaw(t, `{"==":[1,2]}`)
	snap := &Snapshot{
		Nodes: []SnapshotNode{{NodeKey: "decide"}, {NodeKey: "branchA"}},
		Edges: []SnapshotEdge{
			{FromNodeKey: "decide", ToNodeKey: "branchA", Priority: 1, Condition: never},
		},
	}
	got := snap.NextAfterAll("decide", map[string]any{})
	if len(got) != 0 {
		t.Fatalf("no match + no catch-all → empty; got %+v", got)
	}
}

// TestNextAfterAll_VarMissingFallsThrough (PRD R5): var reference against
// missing data evaluates to nil → condition does not match → catch-all path.
func TestNextAfterAll_VarMissingFallsThrough(t *testing.T) {
	t.Parallel()
	// References verdict.result but evalCtx has no verdict namespace.
	cond := jsonRaw(t, `{"==":[{"var":"verdict.result"},"pass"]}`)
	snap := &Snapshot{
		Nodes: []SnapshotNode{{NodeKey: "decide"}, {NodeKey: "branchA"}, {NodeKey: "default"}},
		Edges: []SnapshotEdge{
			{FromNodeKey: "decide", ToNodeKey: "branchA", Priority: 1, Condition: cond},
			{FromNodeKey: "decide", ToNodeKey: "default", Priority: 2},
		},
	}
	// Empty map: runtime mode, but verdict namespace absent.
	got := snap.NextAfterAll("decide", map[string]any{})
	if len(got) != 1 || got[0].NodeKey != "default" {
		t.Fatalf("missing var → catch-all default; got %+v", got)
	}
}

// ---------------------------------------------------------------------------
// validateEdgeConditions (P1-2 Wave 2 publish-time validation)
// ---------------------------------------------------------------------------

func TestValidateEdgeConditions_AcceptsAgentWithConditions(t *testing.T) {
	t.Parallel()
	nodes := []NodeInput{
		{NodeKey: "agent", Type: NodeTypeAgent, Name: "A", Config: []byte(`{"agent_selector":"x"}`)},
		{NodeKey: "end", Type: NodeTypeEnd, Name: "End"},
	}
	edges := []EdgeInput{
		{FromNodeKey: "agent", ToNodeKey: "end", Priority: 1,
			Condition: jsonRaw(t, `{"==":[{"var":"verdict.result"},"pass"]}`)},
	}
	if err := validateEdgeConditions(nodes, edges); err != nil {
		t.Fatalf("agent edge with valid condition must PASS: %v", err)
	}
}

func TestValidateEdgeConditions_RejectsConditionOnNonAgent(t *testing.T) {
	t.Parallel()
	nodes := []NodeInput{
		{NodeKey: "accept", Type: NodeTypeAcceptance, Name: "A"},
		{NodeKey: "end", Type: NodeTypeEnd, Name: "End"},
	}
	edges := []EdgeInput{
		{FromNodeKey: "accept", ToNodeKey: "end", Priority: 1,
			Condition: jsonRaw(t, `{"==":[1,1]}`)},
	}
	err := validateEdgeConditions(nodes, edges)
	if err == nil || !strings.Contains(err.Error(), "only agent edges may") {
		t.Fatalf("non-agent edge with condition = %v, want agent-only rejection", err)
	}
}

func TestValidateEdgeConditions_RejectsInvalidJSONLogic(t *testing.T) {
	t.Parallel()
	nodes := []NodeInput{
		{NodeKey: "agent", Type: NodeTypeAgent, Name: "A", Config: []byte(`{"agent_selector":"x"}`)},
		{NodeKey: "end", Type: NodeTypeEnd, Name: "End"},
	}
	edges := []EdgeInput{
		{FromNodeKey: "agent", ToNodeKey: "end", Priority: 1,
			Condition: jsonRaw(t, `{"unknown_op":[1,1]}`)},
	}
	err := validateEdgeConditions(nodes, edges)
	if err == nil || !strings.Contains(err.Error(), "condition invalid") {
		t.Fatalf("invalid JSONLogic = %v, want schema rejection", err)
	}
}

// TestValidateEdgeConditions_RejectsMultipleCatchAllOnAgent (AC10): an agent
// node with two condition=nil edges is ambiguous routing → publish 422.
func TestValidateEdgeConditions_RejectsMultipleCatchAllOnAgent(t *testing.T) {
	t.Parallel()
	nodes := []NodeInput{
		{NodeKey: "agent", Type: NodeTypeAgent, Name: "A", Config: []byte(`{"agent_selector":"x"}`)},
		{NodeKey: "end1", Type: NodeTypeEnd, Name: "End1"},
		{NodeKey: "end2", Type: NodeTypeEnd, Name: "End2"},
	}
	edges := []EdgeInput{
		{FromNodeKey: "agent", ToNodeKey: "end1", Priority: 1},
		{FromNodeKey: "agent", ToNodeKey: "end2", Priority: 2},
	}
	err := validateEdgeConditions(nodes, edges)
	if err == nil || !strings.Contains(err.Error(), "catch-all") {
		t.Fatalf("two catch-all edges on agent = %v, want AC10 rejection", err)
	}
}

// TestValidateEdgeConditions_FanOutMultiEdgeNotCatchAll (AC10 scoping): a
// fan_out node may have multiple condition=nil edges — fan_out's branching
// semantics are structural (P1-1), not conditional routing. AC10 applies
// only to agent nodes.
func TestValidateEdgeConditions_FanOutMultiEdgeNotCatchAll(t *testing.T) {
	t.Parallel()
	nodes := []NodeInput{
		{NodeKey: "fan", Type: NodeTypeFanOut, Name: "F", Config: []byte(`{"items_field":"x"}`)},
		{NodeKey: "b1", Type: NodeTypeAgent, Name: "B1", Config: []byte(`{"agent_selector":"x"}`)},
		{NodeKey: "b2", Type: NodeTypeAgent, Name: "B2", Config: []byte(`{"agent_selector":"y"}`)},
	}
	edges := []EdgeInput{
		{FromNodeKey: "fan", ToNodeKey: "b1", Priority: 1},
		{FromNodeKey: "fan", ToNodeKey: "b2", Priority: 2},
	}
	if err := validateEdgeConditions(nodes, edges); err != nil {
		t.Fatalf("fan_out multi-edge must NOT trigger AC10 (not conditional routing): %v", err)
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
	// P1-2: nil evalCtx = topology mode; without conditions the return
	// shape (≤1 element) matches the P0 invariant exactly.
	for i, key := range chain {
		got := snap.NextAfterAll(key, nil)
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
		got := snap.NextAfterAll(key, nil)
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
