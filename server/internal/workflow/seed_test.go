package workflow

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/util"
)

// seed_test.go — seed template coverage (R8 / AC7 structure half):
// standard 9-node / bugfix 6-node shapes, role + separation invariants,
// idempotent re-seed, and preflight failures (unresolvable selector,
// evaluator=executor) that must not write any row.

// seedSelectors binds the four seed roles to four fresh fixture agents.
func seedSelectors(f *testFixture) SeedAgentSelectors {
	f.t.Helper()
	return SeedAgentSelectors{
		Planner:     f.createAgent("Seed Planner"),
		Implementer: f.createAgent("Seed Implementer"),
		GateRunner:  f.createAgent("Seed Gate Runner"),
		Reviewer:    f.createAgent("Seed Reviewer"),
	}
}

// seedTemplates is the test shorthand for a full seed call.
func seedTemplates(t *testing.T, f *testFixture, selectors SeedAgentSelectors) []SeedResult {
	t.Helper()
	results, err := f.templates.SeedTemplates(context.Background(), SeedTemplatesParams{
		WorkspaceID: util.MustParseUUID(f.workspaceID),
		CreatedBy:   util.MustParseUUID(f.userID),
		Selectors:   selectors,
	})
	if err != nil {
		t.Fatalf("seed templates: %v", err)
	}
	return results
}

// templateByKey loads the workspace's template detail for a seeded key.
func templateByKey(t *testing.T, f *testFixture, key string) *TemplateDetail {
	t.Helper()
	tmpls, err := f.templates.ListTemplates(context.Background(), util.MustParseUUID(f.workspaceID))
	if err != nil {
		t.Fatalf("list templates: %v", err)
	}
	for _, tm := range tmpls {
		if tm.Key == key {
			detail, err := f.templates.GetTemplate(context.Background(), util.MustParseUUID(f.workspaceID), tm.ID)
			if err != nil {
				t.Fatalf("get template %q: %v", key, err)
			}
			return detail
		}
	}
	t.Fatalf("template %q not found", key)
	return nil
}

// nodeConfigByKey maps a seeded template's nodes to their parsed configs.
func nodeConfigByKey(t *testing.T, d *TemplateDetail) map[string]NodeConfig {
	t.Helper()
	out := map[string]NodeConfig{}
	for _, n := range d.Nodes {
		cfg, err := ParseNodeConfig(n.Config)
		if err != nil {
			t.Fatalf("parse node %q config: %v", n.NodeKey, err)
		}
		out[n.NodeKey] = cfg
	}
	return out
}

func TestSeedTemplatesStructure(t *testing.T) {
	f := newTestFixture(t)
	sel := seedSelectors(f)
	results := seedTemplates(t, f, sel)

	if len(results) != 2 || !results[0].Seeded || !results[1].Seeded {
		t.Fatalf("results = %+v, want both seeded", results)
	}

	standard := templateByKey(t, f, SeedTemplateKeyStandard)
	if standard.Template.Status != "published" {
		t.Fatalf("standard status = %q, want published", standard.Template.Status)
	}
	wantStandardChain := []string{"plan", "plan-gate", "spec-freeze", "implement", "baseline-gate", "api-gate", "review", "final-acceptance", "done"}
	if len(standard.Nodes) != 9 {
		t.Fatalf("standard nodes = %d, want 9", len(standard.Nodes))
	}
	if len(standard.Edges) != 8 {
		t.Fatalf("standard edges = %d, want 8 (linear chain)", len(standard.Edges))
	}
	stdCfg := nodeConfigByKey(t, standard)
	typeByKey := map[string]string{}
	for _, n := range standard.Nodes {
		typeByKey[n.NodeKey] = n.Type
	}
	wantTypes := map[string]string{
		"plan": NodeTypeAgent, "plan-gate": NodeTypeAgent, "spec-freeze": NodeTypeAcceptance,
		"implement": NodeTypeAgent, "baseline-gate": NodeTypeAgent, "api-gate": NodeTypeAgent,
		"review": NodeTypeAgent, "final-acceptance": NodeTypeAcceptance, "done": NodeTypeEnd,
	}
	for _, key := range wantStandardChain {
		if typeByKey[key] != wantTypes[key] {
			t.Fatalf("standard node %q type = %q, want %q", key, typeByKey[key], wantTypes[key])
		}
	}
	// Roles: executors vs evaluators per the verdict actor model (§4.3).
	for _, key := range []string{"plan", "implement"} {
		if stdCfg[key].EffectiveRole() != RoleExecutor {
			t.Fatalf("standard node %q role = %q, want executor", key, stdCfg[key].EffectiveRole())
		}
	}
	for _, key := range []string{"plan-gate", "baseline-gate", "api-gate", "review"} {
		if stdCfg[key].EffectiveRole() != RoleEvaluator {
			t.Fatalf("standard node %q role = %q, want evaluator", key, stdCfg[key].EffectiveRole())
		}
	}
	// Separation: publish froze distinct agent UUIDs — gates share the
	// gate-runner agent, review uses the reviewer agent, and neither may
	// equal an executor's agent.
	planAgent, implAgent := stdCfg["plan"].AgentID, stdCfg["implement"].AgentID
	gateAgent, reviewAgent := stdCfg["baseline-gate"].AgentID, stdCfg["review"].AgentID
	if planAgent == "" || implAgent == "" || gateAgent == "" || reviewAgent == "" {
		t.Fatalf("agent_id not frozen at publish: plan=%q implement=%q gate=%q review=%q", planAgent, implAgent, gateAgent, reviewAgent)
	}
	if planAgent == implAgent {
		t.Fatalf("plan and implement should bind distinct executors (planner vs implementer)")
	}
	if gateAgent == planAgent || gateAgent == implAgent || reviewAgent == planAgent || reviewAgent == implAgent {
		t.Fatalf("evaluator agent equals an executor agent: gate=%q review=%q plan=%q implement=%q", gateAgent, reviewAgent, planAgent, implAgent)
	}
	if stdCfg["plan-gate"].AgentID != gateAgent || stdCfg["api-gate"].AgentID != gateAgent {
		t.Fatalf("gate stages should share the gate-runner agent")
	}
	// Retry budget + human gates (D-12).
	for _, key := range wantStandardChain {
		if stdCfg[key].EffectiveMaxAttempts() != 3 {
			t.Fatalf("standard node %q max_attempts = %d, want 3", key, stdCfg[key].EffectiveMaxAttempts())
		}
		if stdCfg[key].AutoPass {
			t.Fatalf("standard node %q auto_pass = true, want false (D-12)", key)
		}
		if stdCfg[key].Instructions == "" && wantTypes[key] != NodeTypeEnd {
			t.Fatalf("standard node %q has no instructions", key)
		}
	}
	// Exit-field schemas carry the 准出 contract.
	requiredOf := func(key string) map[string]bool {
		out := map[string]bool{}
		if stdCfg[key].ExitFields == nil {
			return out
		}
		for _, fld := range stdCfg[key].ExitFields.Fields {
			out[fld.Name] = fld.Required
		}
		return out
	}
	planReq := requiredOf("plan")
	for _, name := range []string{"prd_url", "design_url", "business_test_cases_url"} {
		if !planReq[name] {
			t.Fatalf("plan exit field %q missing or not required: %v", name, planReq)
		}
	}
	implReq := requiredOf("implement")
	for _, name := range []string{"pr_url", "branch", "summary"} {
		if !implReq[name] {
			t.Fatalf("implement exit field %q missing or not required: %v", name, implReq)
		}
	}
	if !requiredOf("baseline-gate")["baseline_diff_url"] || !requiredOf("plan-gate")["gate_report_url"] || !requiredOf("api-gate")["gate_report_url"] {
		t.Fatalf("gate exit fields missing required report fields")
	}
	if !requiredOf("review")["decision"] {
		t.Fatalf("review exit fields must carry required decision (命名铁律)")
	}

	bugfix := templateByKey(t, f, SeedTemplateKeyBugfix)
	if len(bugfix.Nodes) != 6 {
		t.Fatalf("bugfix nodes = %d, want 6", len(bugfix.Nodes))
	}
	if len(bugfix.Edges) != 5 {
		t.Fatalf("bugfix edges = %d, want 5", len(bugfix.Edges))
	}
	bugCfg := nodeConfigByKey(t, bugfix)
	bugTypeByKey := map[string]string{}
	for _, n := range bugfix.Nodes {
		bugTypeByKey[n.NodeKey] = n.Type
	}
	wantBugTypes := map[string]string{
		"plan-lite": NodeTypeAgent, "implement": NodeTypeAgent, "baseline-gate": NodeTypeAgent,
		"review": NodeTypeAgent, "final-acceptance": NodeTypeAcceptance, "done": NodeTypeEnd,
	}
	for key, want := range wantBugTypes {
		if bugTypeByKey[key] != want {
			t.Fatalf("bugfix node %q type = %q, want %q", key, bugTypeByKey[key], want)
		}
	}
	if bugCfg["final-acceptance"].AutoPass {
		t.Fatalf("bugfix final-acceptance auto_pass = true, want false (default human acceptance, D-12)")
	}
	bugPlanReq := map[string]bool{}
	for _, fld := range bugCfg["plan-lite"].ExitFields.Fields {
		bugPlanReq[fld.Name] = fld.Required
	}
	if !bugPlanReq["prd_url"] || !bugPlanReq["business_test_cases_url"] || bugPlanReq["design_url"] {
		t.Fatalf("plan-lite exit fields = %v, want prd/business-test-cases required and design optional", bugPlanReq)
	}
}

func TestSeedTemplatesIdempotent(t *testing.T) {
	f := newTestFixture(t)
	sel := seedSelectors(f)
	first := seedTemplates(t, f, sel)
	if !first[0].Seeded || !first[1].Seeded {
		t.Fatalf("first seed = %+v, want both seeded", first)
	}

	second := seedTemplates(t, f, sel)
	for _, r := range second {
		if r.Seeded {
			t.Fatalf("second seed = %+v, want every key skipped", second)
		}
	}
	tmpls, err := f.templates.ListTemplates(context.Background(), util.MustParseUUID(f.workspaceID))
	if err != nil {
		t.Fatalf("list templates: %v", err)
	}
	if len(tmpls) != 2 {
		t.Fatalf("templates after re-seed = %d, want exactly 2 (no duplicates)", len(tmpls))
	}
}

func TestSeedTemplatesUnresolvedSelectorWritesNothing(t *testing.T) {
	f := newTestFixture(t)
	sel := seedSelectors(f)
	sel.GateRunner = "no-such-gate-agent"

	_, err := f.templates.SeedTemplates(context.Background(), SeedTemplatesParams{
		WorkspaceID: util.MustParseUUID(f.workspaceID),
		CreatedBy:   util.MustParseUUID(f.userID),
		Selectors:   sel,
	})
	if err == nil || !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("seed = %v, want ErrAgentNotFound", err)
	}
	if !strings.Contains(err.Error(), "gate-runner") {
		t.Fatalf("error should name the failing role: %v", err)
	}
	tmpls, lerr := f.templates.ListTemplates(context.Background(), util.MustParseUUID(f.workspaceID))
	if lerr != nil {
		t.Fatalf("list templates: %v", lerr)
	}
	if len(tmpls) != 0 {
		t.Fatalf("preflight failure left %d templates behind, want 0", len(tmpls))
	}
}

func TestSeedTemplatesSeparationViolationWritesNothing(t *testing.T) {
	f := newTestFixture(t)
	sel := seedSelectors(f)
	// Gate runner == implementer: publish would reject this AFTER a draft
	// exists; the seed preflight must reject it BEFORE any write.
	sel.GateRunner = sel.Implementer

	_, err := f.templates.SeedTemplates(context.Background(), SeedTemplatesParams{
		WorkspaceID: util.MustParseUUID(f.workspaceID),
		CreatedBy:   util.MustParseUUID(f.userID),
		Selectors:   sel,
	})
	var sepErr *EvaluatorSeparationError
	if !errors.As(err, &sepErr) {
		t.Fatalf("seed = %v, want EvaluatorSeparationError", err)
	}
	tmpls, lerr := f.templates.ListTemplates(context.Background(), util.MustParseUUID(f.workspaceID))
	if lerr != nil {
		t.Fatalf("list templates: %v", lerr)
	}
	if len(tmpls) != 0 {
		t.Fatalf("separation violation left %d templates behind, want 0", len(tmpls))
	}
}

func TestSeedTemplatesDefaultSelectors(t *testing.T) {
	f := newTestFixture(t)
	// Agents named exactly like the defaults: an empty-selector seed must
	// bind them (the CLI relies on this when no --*-agent flags are passed).
	f.createAgent(DefaultSeedPlannerAgent)
	f.createAgent(DefaultSeedImplementerAgent)
	f.createAgent(DefaultSeedGateAgent)
	f.createAgent(DefaultSeedReviewAgent)

	results := seedTemplates(t, f, SeedAgentSelectors{})
	if !results[0].Seeded || !results[1].Seeded {
		t.Fatalf("default-selector seed = %+v, want both seeded", results)
	}
	standard := templateByKey(t, f, SeedTemplateKeyStandard)
	stdCfg := nodeConfigByKey(t, standard)
	if stdCfg["plan"].AgentID == stdCfg["baseline-gate"].AgentID {
		t.Fatalf("default bindings must keep evaluator ≠ executor")
	}
}
