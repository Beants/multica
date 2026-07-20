package workflow

// template_fanout_test.go — P1-1 Wave 1 unit tests for parseSubtasks,
// ValidateFanOutConfig, ValidateConvergePairing, and the FailPolicy
// NodeConfig plumbing. Pure unit tests (no DB) so they run in the fast
// lane alongside template_dag_test.go.

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// parseSubtasks
// ---------------------------------------------------------------------------

func TestParseSubtasks_Valid(t *testing.T) {
	t.Parallel()
	raw := []any{
		map[string]any{
			"title":          "Implement API X",
			"instructions":   "See spec section 2",
			"agent_selector": "backend-bot",
			"priority":       "high",
			"due_date":       "2026-08-01T00:00:00Z",
			"labels":         []any{"backend", "p1"},
		},
		map[string]any{
			"title":          "Update docs",
			"instructions":   "Cover new endpoint",
			"agent_selector": "docs-bot",
		},
	}
	items, errs := parseSubtasks(raw)
	if len(errs) != 0 {
		t.Fatalf("expected no errors, got %+v", errs)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if items[0].Title != "Implement API X" {
		t.Errorf("items[0].Title = %q", items[0].Title)
	}
	if items[0].Priority != "high" {
		t.Errorf("items[0].Priority = %q", items[0].Priority)
	}
	if len(items[0].Labels) != 2 {
		t.Errorf("items[0].Labels len = %d", len(items[0].Labels))
	}
	if items[1].Priority != "" {
		t.Errorf("items[1].Priority = %q, want empty default", items[1].Priority)
	}
}

func TestParseSubtasks_MissingTitle(t *testing.T) {
	t.Parallel()
	raw := []any{
		map[string]any{
			"instructions":   "x",
			"agent_selector": "bot",
		},
	}
	_, errs := parseSubtasks(raw)
	if !containsSubtaskError(errs, 0, "title", "missing") {
		t.Fatalf("expected missing title on item 0, got %+v", errs)
	}
}

func TestParseSubtasks_MissingInstructions(t *testing.T) {
	t.Parallel()
	raw := []any{
		map[string]any{
			"title":          "T",
			"agent_selector": "bot",
		},
	}
	_, errs := parseSubtasks(raw)
	if !containsSubtaskError(errs, 0, "instructions", "missing") {
		t.Fatalf("expected missing instructions on item 0, got %+v", errs)
	}
}

func TestParseSubtasks_MissingAgentSelector(t *testing.T) {
	t.Parallel()
	raw := []any{
		map[string]any{
			"title":        "T",
			"instructions": "x",
		},
	}
	_, errs := parseSubtasks(raw)
	if !containsSubtaskError(errs, 0, "agent_selector", "missing") {
		t.Fatalf("expected missing agent_selector on item 0, got %+v", errs)
	}
}

func TestParseSubtasks_InvalidPriority(t *testing.T) {
	t.Parallel()
	raw := []any{
		map[string]any{
			"title":          "T",
			"instructions":   "x",
			"agent_selector": "bot",
			"priority":       "ridiculous",
		},
	}
	_, errs := parseSubtasks(raw)
	if !containsSubtaskError(errs, 0, "priority", "invalid") {
		t.Fatalf("expected invalid priority on item 0, got %+v", errs)
	}
}

func TestParseSubtasks_MixedValidInvalid(t *testing.T) {
	t.Parallel()
	raw := []any{
		map[string]any{
			"title":          "OK",
			"instructions":   "ok",
			"agent_selector": "ok-bot",
		},
		map[string]any{
			// missing title + agent_selector
			"instructions": "bad",
		},
		map[string]any{
			"title":          "OK2",
			"instructions":   "ok2",
			"agent_selector": "ok2-bot",
			"priority":       "low",
		},
	}
	items, errs := parseSubtasks(raw)
	// Items slice includes every parseable entry (errors do not remove
	// items from the slice; only structural JSON failures skip).
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d (%+v)", len(items), items)
	}
	// Item 1 has two missing-field errors.
	var missingOn1 []SubtaskFieldError
	for _, e := range errs {
		if e.ItemIndex == 1 {
			missingOn1 = append(missingOn1, e)
		}
	}
	if len(missingOn1) != 2 {
		t.Fatalf("expected 2 errors on item 1, got %d (%+v)", len(missingOn1), missingOn1)
	}
}

func TestParseSubtasks_Empty(t *testing.T) {
	t.Parallel()
	items, errs := parseSubtasks(nil)
	if len(items) != 0 || len(errs) != 0 {
		t.Fatalf("nil input: items=%d errs=%d", len(items), len(errs))
	}
}

// ---------------------------------------------------------------------------
// ValidateFanOutConfig
// ---------------------------------------------------------------------------

func TestValidateFanOutConfig_NoItemsField(t *testing.T) {
	t.Parallel()
	fanOut := typedNode("fanout", NodeTypeFanOut, NodeConfig{})
	upstream := agentNode("up", RoleExecutor, "Agent", NodeConfig{
		ExitFields: &ExitFieldsSchema{Fields: []ExitFieldSpec{{Name: "subtasks", Type: "array"}}},
	})
	nodes := []NodeInput{upstream, fanOut}
	edges := dagEdges("up", "fanout")
	err := ValidateFanOutConfig(fanOut, NodeConfig{}, nodes, edges)
	if err == nil || !strings.Contains(err.Error(), "requires config.items_field") {
		t.Fatalf("got err = %v, want items_field required", err)
	}
}

func TestValidateFanOutConfig_ItemsFieldNotArray(t *testing.T) {
	t.Parallel()
	// Upstream declares the field but as string, not array.
	upstream := agentNode("up", RoleExecutor, "Agent", NodeConfig{
		ExitFields: &ExitFieldsSchema{Fields: []ExitFieldSpec{{Name: "subtasks", Type: "string"}}},
	})
	fanOut := typedNode("fanout", NodeTypeFanOut, NodeConfig{ItemsField: "subtasks"})
	nodes := []NodeInput{upstream, fanOut}
	edges := dagEdges("up", "fanout")
	err := ValidateFanOutConfig(fanOut, NodeConfig{ItemsField: "subtasks"}, nodes, edges)
	if err == nil || !strings.Contains(err.Error(), "not declared as array") {
		t.Fatalf("got err = %v, want 'not declared as array'", err)
	}
}

func TestValidateFanOutConfig_ItemsFieldOnUpstream(t *testing.T) {
	t.Parallel()
	upstream := agentNode("up", RoleExecutor, "Agent", NodeConfig{
		ExitFields: &ExitFieldsSchema{Fields: []ExitFieldSpec{{Name: "subtasks", Type: "array"}}},
	})
	fanOut := typedNode("fanout", NodeTypeFanOut, NodeConfig{ItemsField: "subtasks"})
	nodes := []NodeInput{upstream, fanOut}
	edges := dagEdges("up", "fanout")
	cfg, err := ParseNodeConfig(fanOut.Config)
	if err != nil {
		t.Fatalf("parse fan_out config: %v", err)
	}
	if err := ValidateFanOutConfig(fanOut, cfg, nodes, edges); err != nil {
		t.Fatalf("expected PASS, got %v", err)
	}
}

func TestValidateFanOutConfig_ItemsFieldAnyType(t *testing.T) {
	t.Parallel()
	// type=any is accepted for forward-compat with loosely-typed schemas.
	upstream := agentNode("up", RoleExecutor, "Agent", NodeConfig{
		ExitFields: &ExitFieldsSchema{Fields: []ExitFieldSpec{{Name: "payload", Type: "any"}}},
	})
	fanOut := typedNode("fanout", NodeTypeFanOut, NodeConfig{ItemsField: "payload"})
	nodes := []NodeInput{upstream, fanOut}
	edges := dagEdges("up", "fanout")
	cfg, _ := ParseNodeConfig(fanOut.Config)
	if err := ValidateFanOutConfig(fanOut, cfg, nodes, edges); err != nil {
		t.Fatalf("expected PASS for type=any, got %v", err)
	}
}

func TestValidateFanOutConfig_NoUpstream(t *testing.T) {
	t.Parallel()
	// fan_out at the start of the graph (no inbound edges). Should reject
	// because there's no upstream to declare items_field.
	fanOut := typedNode("fanout", NodeTypeFanOut, NodeConfig{ItemsField: "subtasks"})
	branch := agentNode("branch", RoleExecutor, "Agent", NodeConfig{})
	nodes := []NodeInput{fanOut, branch}
	edges := dagEdges("fanout", "branch")
	cfg, _ := ParseNodeConfig(fanOut.Config)
	err := ValidateFanOutConfig(fanOut, cfg, nodes, edges)
	if err == nil || !strings.Contains(err.Error(), "no upstream node") {
		t.Fatalf("got err = %v, want 'no upstream node'", err)
	}
}

func TestValidateFanOutConfig_UpstreamMissingExitFieldsSchema(t *testing.T) {
	t.Parallel()
	// Upstream agent has no ExitFields at all.
	upstream := agentNode("up", RoleExecutor, "Agent", NodeConfig{})
	fanOut := typedNode("fanout", NodeTypeFanOut, NodeConfig{ItemsField: "subtasks"})
	nodes := []NodeInput{upstream, fanOut}
	edges := dagEdges("up", "fanout")
	cfg, _ := ParseNodeConfig(fanOut.Config)
	err := ValidateFanOutConfig(fanOut, cfg, nodes, edges)
	if err == nil || !strings.Contains(err.Error(), "not declared as array") {
		t.Fatalf("got err = %v, want 'not declared as array'", err)
	}
}

// ---------------------------------------------------------------------------
// ValidateConvergePairing
// ---------------------------------------------------------------------------

func TestValidateConvergePairing_Paired(t *testing.T) {
	t.Parallel()
	nodes := []NodeInput{
		fanOutNode("fanout"),
		agentNode("branchA", RoleExecutor, "Agent", NodeConfig{}),
		convergeNode("converge"),
		typedNode("end", NodeTypeEnd, NodeConfig{}),
	}
	edges := dagEdges(
		"fanout", "branchA",
		"branchA", "converge",
		"converge", "end",
	)
	if err := ValidateConvergePairing(nodes, edges); err != nil {
		t.Fatalf("expected PASS, got %v", err)
	}
}

func TestValidateConvergePairing_OrphanFanOut(t *testing.T) {
	t.Parallel()
	// fan_out whose branches never re-converge.
	nodes := []NodeInput{
		fanOutNode("fanout"),
		agentNode("branchA", RoleExecutor, "Agent", NodeConfig{}),
		typedNode("endA", NodeTypeEnd, NodeConfig{}),
	}
	edges := dagEdges(
		"fanout", "branchA",
		"branchA", "endA",
	)
	err := ValidateConvergePairing(nodes, edges)
	if err == nil || !strings.Contains(err.Error(), "no downstream path to a converge") {
		t.Fatalf("got err = %v, want 'no downstream path to a converge'", err)
	}
}

func TestValidateConvergePairing_OrphanConverge(t *testing.T) {
	t.Parallel()
	// converge with no inbound from a fan_out.
	nodes := []NodeInput{
		agentNode("upstream", RoleExecutor, "Agent", NodeConfig{}),
		convergeNode("converge"),
		typedNode("end", NodeTypeEnd, NodeConfig{}),
	}
	edges := dagEdges(
		"upstream", "converge",
		"converge", "end",
	)
	err := ValidateConvergePairing(nodes, edges)
	if err == nil || !strings.Contains(err.Error(), "no upstream path from a fan_out") {
		t.Fatalf("got err = %v, want 'no upstream path from a fan_out'", err)
	}
}

func TestValidateConvergePairing_NoFanOutNoConverge(t *testing.T) {
	t.Parallel()
	// P0 linear template — pairing check must be a no-op.
	nodes := []NodeInput{
		agentNode("a", RoleExecutor, "Agent", NodeConfig{}),
		typedNode("end", NodeTypeEnd, NodeConfig{}),
	}
	edges := dagEdges("a", "end")
	if err := ValidateConvergePairing(nodes, edges); err != nil {
		t.Fatalf("P0 template must PASS pairing check, got %v", err)
	}
}

func TestValidateConvergePairing_MultipleFanOutsToOneConverge(t *testing.T) {
	t.Parallel()
	// Two fan_outs feeding a single converge — legal N:1 shape.
	nodes := []NodeInput{
		fanOutNode("fo1"),
		fanOutNode("fo2"),
		convergeNode("conv"),
		typedNode("end", NodeTypeEnd, NodeConfig{}),
	}
	edges := dagEdges(
		"fo1", "conv",
		"fo2", "conv",
		"conv", "end",
	)
	if err := ValidateConvergePairing(nodes, edges); err != nil {
		t.Fatalf("N:1 fan_out→converge must PASS, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// ParseNodeConfig FailPolicy enum + EffectiveFailPolicy
// ---------------------------------------------------------------------------

func TestParseNodeConfig_FailPolicyEnum(t *testing.T) {
	t.Parallel()
	for _, fp := range []string{FailPolicyFail, FailPolicyBlocked, FailPolicyRework, ""} {
		raw := []byte(`{"fail_policy":"` + fp + `"}`)
		if fp == "" {
			raw = []byte(`{}`)
		}
		cfg, err := ParseNodeConfig(raw)
		if err != nil {
			t.Fatalf("fail_policy %q: unexpected err %v", fp, err)
		}
		if cfg.FailPolicy != fp {
			t.Fatalf("fail_policy %q: got back %q", fp, cfg.FailPolicy)
		}
	}

	// Unknown enum value must reject.
	if _, err := ParseNodeConfig([]byte(`{"fail_policy":"bogus"}`)); err == nil {
		t.Fatalf("unknown fail_policy must reject")
	}
}

func TestNodeConfig_EffectiveFailPolicy(t *testing.T) {
	t.Parallel()
	cases := []struct {
		input string
		want  string
	}{
		{"", FailPolicyRework},
		{FailPolicyFail, FailPolicyFail},
		{FailPolicyBlocked, FailPolicyBlocked},
		{FailPolicyRework, FailPolicyRework},
	}
	for _, c := range cases {
		got := NodeConfig{FailPolicy: c.input}.EffectiveFailPolicy()
		if got != c.want {
			t.Errorf("EffectiveFailPolicy(%q) = %q, want %q", c.input, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// validateTemplateGraph integration (sanity-check the Wave 1 hooks)
// ---------------------------------------------------------------------------

func TestValidateGraphFanOut_FullValidTemplate(t *testing.T) {
	t.Parallel()
	// End-to-end shape: upstream agent (declares items_field array) →
	// fan_out (references items_field) → [branchA, branchB] → converge →
	// end. validateTemplateGraph must accept it under Wave 1 rules.
	upstream := agentNode("upstream", RoleExecutor, "Agent", NodeConfig{
		ExitFields: &ExitFieldsSchema{Fields: []ExitFieldSpec{{Name: "subtasks", Type: "array"}}},
	})
	fanOut := typedNode("fanout", NodeTypeFanOut, NodeConfig{
		ItemsField: "subtasks",
		FailPolicy: FailPolicyRework,
	})
	branchA := agentNode("branchA", RoleExecutor, "Agent", NodeConfig{})
	branchB := agentNode("branchB", RoleExecutor, "Agent", NodeConfig{})
	converge := convergeNode("converge")
	end := typedNode("end", NodeTypeEnd, NodeConfig{})
	nodes := []NodeInput{upstream, fanOut, branchA, branchB, converge, end}
	edges := dagEdges(
		"upstream", "fanout",
		"fanout", "branchA",
		"fanout", "branchB",
		"branchA", "converge",
		"branchB", "converge",
		"converge", "end",
	)
	if err := validateTemplateGraph(nodes, edges); err != nil {
		t.Fatalf("full fan_out template must validate: %v", err)
	}
}

func TestValidateGraphFanOut_RejectMissingItemsField(t *testing.T) {
	t.Parallel()
	// fan_out with empty config — Wave 1 must reject via the
	// validateTemplateGraph hook.
	nodes := []NodeInput{
		agentNode("upstream", RoleExecutor, "Agent", NodeConfig{}),
		fanOutNode("fanout"),
		agentNode("branch", RoleExecutor, "Agent", NodeConfig{}),
		convergeNode("converge"),
		typedNode("end", NodeTypeEnd, NodeConfig{}),
	}
	edges := dagEdges(
		"upstream", "fanout",
		"fanout", "branch",
		"branch", "converge",
		"converge", "end",
	)
	err := validateTemplateGraph(nodes, edges)
	if err == nil || !strings.Contains(err.Error(), "items_field") {
		t.Fatalf("got err = %v, want items_field rejection", err)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// containsSubtaskError asserts the parsed errors contain an entry matching
// the given item index, field name, and code.
func containsSubtaskError(errs []SubtaskFieldError, itemIdx int, name, code string) bool {
	for _, e := range errs {
		if e.ItemIndex == itemIdx && e.Name == name && e.Code == code {
			return true
		}
	}
	return false
}
