package workflow

// yaml_import_test.go — P1-8 harness YAML import unit + DB-backed tests
// (PRD AC1-AC5 + R1-R5 coverage). The pure-parse / pure-convert tests
// are hermetic; the publishable + duplicate-key tests hit the test DB
// via newTestFixture (skipped when Postgres is unreachable, matching
// the rest of the workflow package's test contract).
//
// The YAML literals below are embedded as Go raw string constants
// rather than read from harness/pipeline/*.yaml so the test does not
// depend on the test process's working directory. Keep them in sync
// with the canonical YAMLs when those change.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/util"
)

// standardYAML mirrors multica/harness/pipeline/standard.yaml.
const standardYAML = `# Standard Pipeline — 6 stages, 2 human gates
pipeline:
  name: standard
  description: "Full pipeline: plan -> gate -> implement -> gate -> review -> accept"
  stages:
    - id: 1
      name: Plan
      role: planner
      initial_status: todo
      produces: [prd.md, design.md, business-test-cases.md]
    - id: 2
      name: Plan Gate
      role: gate-runner
      initial_status: backlog
      gate:
        type: script
        script: plan_contract_check.py
        hard: true
        on_fail: rework stage 1
    - id: 3
      name: Implement
      role: implementer
      initial_status: backlog
      produces: [code, tech-test-cases.md, baseline/after.json]
    - id: 4
      name: Baseline Gate
      role: gate-runner
      initial_status: backlog
      gate:
        type: script
        script: baseline.py diff
        hard: true
        on_fail: rework stage 3
    - id: 5
      name: Review
      role: reviewer
      initial_status: backlog
      produces: [review-verdict.yaml]
      gate:
        type: soft
        hard: false
        on_fail: record findings, continue
    - id: 6
      name: Acceptance
      role: human
      initial_status: backlog
  human_gates:
    - after_stage: 1
      name: Spec Freeze
      description: "Human reviews prd.md + business-test-cases.md"
    - at_stage: 6
      name: Final Acceptance
      description: "Human verifies all test cases pass"
  circuit_breaker:
    threshold: 3
    action: assign parent to human
`

// bugfixYAML mirrors multica/harness/pipeline/bugfix.yaml.
const bugfixYAML = `# Bugfix Pipeline — 4 stages, human gate optional
pipeline:
  name: bugfix
  description: "Short pipeline: plan -> implement+baseline -> review -> accept"
  stages:
    - id: 1
      name: Plan (lite)
      role: planner
      initial_status: todo
      produces: [prd.md, business-test-cases.md]
    - id: 2
      name: Implement + Baseline
      role: implementer
      initial_status: backlog
      produces: [code, tech-test-cases.md, baseline/after.json]
      gate:
        type: script
        script: baseline.py diff
        hard: true
        on_fail: rework stage 2
    - id: 3
      name: Review (soft)
      role: reviewer
      initial_status: backlog
      produces: [review-verdict.yaml]
      gate:
        type: soft
        hard: false
    - id: 4
      name: Acceptance
      role: human
      initial_status: backlog
      auto_pass: true
  human_gates:
    - at_stage: 4
      name: Acceptance
      description: "Human verifies fix"
      auto_pass_condition: "review verdict == APPROVED and baseline diff blocking == false"
  circuit_breaker:
    threshold: 3
    action: assign parent to human
`

// ---------------------------------------------------------------------------
// R1: YAML schema parsing
// ---------------------------------------------------------------------------

func TestParsePipelineYAML_Standard(t *testing.T) {
	p, err := ParsePipelineYAML([]byte(standardYAML))
	if err != nil {
		t.Fatalf("parse standard: %v", err)
	}
	if p.Pipeline.Name != "standard" {
		t.Fatalf("name = %q, want standard", p.Pipeline.Name)
	}
	if !strings.Contains(p.Pipeline.Description, "plan") {
		t.Fatalf("description = %q, want contains 'plan'", p.Pipeline.Description)
	}
	if len(p.Pipeline.Stages) != 6 {
		t.Fatalf("stages = %d, want 6", len(p.Pipeline.Stages))
	}
	if len(p.Pipeline.HumanGates) != 2 {
		t.Fatalf("human_gates = %d, want 2", len(p.Pipeline.HumanGates))
	}
	// stage 2 carries the hard script gate block.
	s2 := p.Pipeline.Stages[1]
	if s2.Role != "gate-runner" || s2.Gate == nil || s2.Gate.Type != "script" {
		t.Fatalf("stage 2 = %+v, want gate-runner with script gate", s2)
	}
	if s2.Gate.Script != "plan_contract_check.py" {
		t.Fatalf("stage 2 gate.script = %q, want plan_contract_check.py", s2.Gate.Script)
	}
	if s2.Gate.Hard == nil || !*s2.Gate.Hard {
		t.Fatalf("stage 2 gate.hard = %v, want true", s2.Gate.Hard)
	}
	// human_gates[0] anchors after stage 1.
	hg0 := p.Pipeline.HumanGates[0]
	if hg0.AfterStage == nil || *hg0.AfterStage != 1 {
		t.Fatalf("human_gates[0].after_stage = %v, want 1", hg0.AfterStage)
	}
	if hg0.Name != "Spec Freeze" {
		t.Fatalf("human_gates[0].name = %q, want 'Spec Freeze'", hg0.Name)
	}
}

func TestParsePipelineYAML_Bugfix(t *testing.T) {
	p, err := ParsePipelineYAML([]byte(bugfixYAML))
	if err != nil {
		t.Fatalf("parse bugfix: %v", err)
	}
	if p.Pipeline.Name != "bugfix" {
		t.Fatalf("name = %q, want bugfix", p.Pipeline.Name)
	}
	if len(p.Pipeline.Stages) != 4 {
		t.Fatalf("stages = %d, want 4", len(p.Pipeline.Stages))
	}
	if len(p.Pipeline.HumanGates) != 1 {
		t.Fatalf("human_gates = %d, want 1", len(p.Pipeline.HumanGates))
	}
	// stage 2 (Implement + Baseline): implementer WITH a script gate —
	// this is the case the converter splits into agent + gate.
	s2 := p.Pipeline.Stages[1]
	if s2.Role != "implementer" || s2.Gate == nil || s2.Gate.Type != "script" {
		t.Fatalf("stage 2 = %+v, want implementer with script gate", s2)
	}
	// human_gates[0] uses at_stage (not after_stage) — converter
	// treats it as informational (no insertion).
	hg0 := p.Pipeline.HumanGates[0]
	if hg0.AtStage == nil || *hg0.AtStage != 4 {
		t.Fatalf("human_gates[0].at_stage = %v, want 4", hg0.AtStage)
	}
}

func TestParsePipelineYAML_RejectsBad(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want string
	}{
		{"empty", "", "pipeline.name is required"},
		{"no stages", "pipeline:\n  name: x\n", "pipeline.stages is empty"},
		{"dup stage id", "pipeline:\n  name: x\n  stages:\n    - {id: 1, name: A, role: planner}\n    - {id: 1, name: B, role: planner}\n", "duplicate stage id"},
		{"bad role", "pipeline:\n  name: x\n  stages:\n    - {id: 1, name: A, role: wizard}\n", "unknown role"},
		{"zero stage id", "pipeline:\n  name: x\n  stages:\n    - {id: 0, name: A, role: planner}\n", "stages[0].id is required"},
		{"human gate bad anchor", `
pipeline:
  name: x
  stages:
    - {id: 1, name: A, role: planner}
  human_gates:
    - {after_stage: 99, name: Ghost}
`, "references unknown stage id"},
		{"human gate both anchors", `
pipeline:
  name: x
  stages:
    - {id: 1, name: A, role: planner}
  human_gates:
    - {after_stage: 1, at_stage: 1, name: Both}
`, "sets both after_stage and at_stage"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParsePipelineYAML([]byte(tc.yaml))
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want contains %q", err, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// R2/R3: stage → node conversion + edge derivation
// ---------------------------------------------------------------------------

func TestConvertYAMLToTemplate_Standard(t *testing.T) {
	p, err := ParsePipelineYAML([]byte(standardYAML))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	conv, err := ConvertYAMLToTemplate(p, "")
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if conv.Key != "standard" {
		t.Fatalf("key = %q, want standard (defaults to pipeline.name)", conv.Key)
	}
	// Expected chain: plan, spec-freeze, plan-gate, implement,
	// baseline-gate, review, acceptance, end = 8 nodes.
	//
	// PRD AC1 lists 9 nodes (plan/gate/spec-freeze/impl/gate/api-gate/
	// review/accept/end) but that count was copied from the seed.go
	// hand-crafted chain, which adds an api-gate stage that does NOT
	// appear in standard.yaml. The YAML-driven conversion faithfully
	// reflects what the YAML declares; the deviation is documented
	// here so reviewers don't re-introduce a phantom api-gate node.
	wantChain := []struct {
		key  string
		typ  string
		name string
	}{
		{"plan", NodeTypeAgent, "Plan"},
		{"spec-freeze", NodeTypeAcceptance, "Spec Freeze"},
		{"plan-gate", NodeTypeGate, "Plan Gate"},
		{"implement", NodeTypeAgent, "Implement"},
		{"baseline-gate", NodeTypeGate, "Baseline Gate"},
		{"review", NodeTypeAgent, "Review"},
		{"acceptance", NodeTypeAcceptance, "Acceptance"},
		{"end", NodeTypeEnd, "End"},
	}
	if len(conv.Nodes) != len(wantChain) {
		t.Fatalf("nodes = %d, want %d (chain: %+v)", len(conv.Nodes), len(wantChain), nodeKeys(conv.Nodes))
	}
	for i, want := range wantChain {
		got := conv.Nodes[i]
		if got.NodeKey != want.key || got.Type != want.typ || got.Name != want.name {
			t.Fatalf("node[%d] = {key=%q type=%q name=%q}, want {%q %q %q}",
				i, got.NodeKey, got.Type, got.Name, want.key, want.typ, want.name)
		}
	}
	// Edges: linear chain of N-1 = 7 edges, each catch-all.
	if len(conv.Edges) != len(wantChain)-1 {
		t.Fatalf("edges = %d, want %d", len(conv.Edges), len(wantChain)-1)
	}
	for i, e := range conv.Edges {
		if e.FromNodeKey != wantChain[i].key || e.ToNodeKey != wantChain[i+1].key {
			t.Fatalf("edge[%d] = %q → %q, want %q → %q", i, e.FromNodeKey, e.ToNodeKey, wantChain[i].key, wantChain[i+1].key)
		}
		if e.Condition != nil {
			t.Fatalf("edge[%d] condition = %s, want nil (catch-all)", i, e.Condition)
		}
	}
	// Agent + gate node configs carry the converted role / gate_type.
	cfg := parseNodeConfigOrFatal(t, conv.Nodes, "plan")
	if cfg.Role != RoleExecutor {
		t.Fatalf("plan role = %q, want executor", cfg.Role)
	}
	if cfg.ExitFields == nil || len(cfg.ExitFields.Fields) != 1 || cfg.ExitFields.Fields[0].Name != "artifacts" {
		t.Fatalf("plan exit_fields = %+v, want single 'artifacts' field", cfg.ExitFields)
	}
	if cfg.ExitFields.Fields[0].Type != "array" {
		t.Fatalf("plan artifacts type = %q, want array", cfg.ExitFields.Fields[0].Type)
	}
	if !strings.Contains(cfg.ExitFields.Fields[0].Description, "prd.md") {
		t.Fatalf("plan artifacts description = %q, want contains 'prd.md'", cfg.ExitFields.Fields[0].Description)
	}

	planGateCfg := parseNodeConfigOrFatal(t, conv.Nodes, "plan-gate")
	if planGateCfg.GateType != GateTypeScript {
		t.Fatalf("plan-gate gate_type = %q, want script", planGateCfg.GateType)
	}
	if planGateCfg.GateScriptRef != "plan_contract_check.py" {
		t.Fatalf("plan-gate script_ref = %q, want plan_contract_check.py", planGateCfg.GateScriptRef)
	}
	if planGateCfg.GateOnFail != GateOnFailBlock {
		t.Fatalf("plan-gate on_fail = %q, want block (hard=true)", planGateCfg.GateOnFail)
	}

	reviewCfg := parseNodeConfigOrFatal(t, conv.Nodes, "review")
	if reviewCfg.Role != RoleEvaluator {
		t.Fatalf("review role = %q, want evaluator", reviewCfg.Role)
	}
	if reviewCfg.AgentSelector != DefaultImportReviewerAgent {
		t.Fatalf("review agent_selector = %q, want default %q", reviewCfg.AgentSelector, DefaultImportReviewerAgent)
	}
}

func TestConvertYAMLToTemplate_Bugfix(t *testing.T) {
	p, err := ParsePipelineYAML([]byte(bugfixYAML))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	conv, err := ConvertYAMLToTemplate(p, "")
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	// Expected chain (bugfix stage 2 splits into agent + gate):
	// plan-lite, implement-baseline, implement-baseline-gate,
	// review-soft, acceptance, end = 6 nodes (matches PRD AC2).
	wantChain := []struct {
		key string
		typ string
	}{
		{"plan-lite", NodeTypeAgent},
		{"implement-baseline", NodeTypeAgent},
		{"implement-baseline-gate", NodeTypeGate},
		{"review-soft", NodeTypeAgent},
		{"acceptance", NodeTypeAcceptance},
		{"end", NodeTypeEnd},
	}
	if len(conv.Nodes) != len(wantChain) {
		t.Fatalf("nodes = %d, want %d (chain: %+v)", len(conv.Nodes), len(wantChain), nodeKeys(conv.Nodes))
	}
	for i, want := range wantChain {
		got := conv.Nodes[i]
		if got.NodeKey != want.key || got.Type != want.typ {
			t.Fatalf("node[%d] = {key=%q type=%q}, want {%q %q}",
				i, got.NodeKey, got.Type, want.key, want.typ)
		}
	}
	// The split gate's script_ref comes from stage 2's gate.script.
	splitGateCfg := parseNodeConfigOrFatal(t, conv.Nodes, "implement-baseline-gate")
	if splitGateCfg.GateType != GateTypeScript || splitGateCfg.GateScriptRef != "baseline.py diff" {
		t.Fatalf("split gate = %+v, want script/baseline.py diff", splitGateCfg)
	}
	if splitGateCfg.GateOnFail != GateOnFailBlock {
		t.Fatalf("split gate on_fail = %q, want block (hard=true)", splitGateCfg.GateOnFail)
	}
	// Reviewer's soft gate does NOT split — single NodeTypeAgent node.
	reviewCfg := parseNodeConfigOrFatal(t, conv.Nodes, "review-soft")
	if reviewCfg.Role != RoleEvaluator {
		t.Fatalf("review-soft role = %q, want evaluator", reviewCfg.Role)
	}
}

// TestConvertYAMLToTemplate_KeyOverride pins the key-override contract
// (ImportYAMLParams.Key wins over pipeline.name when non-empty).
func TestConvertYAMLToTemplate_KeyOverride(t *testing.T) {
	p, err := ParsePipelineYAML([]byte(bugfixYAML))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	conv, err := ConvertYAMLToTemplate(p, "custom-key")
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if conv.Key != "custom-key" {
		t.Fatalf("key = %q, want custom-key (override)", conv.Key)
	}
	if conv.Name != "bugfix" {
		t.Fatalf("name = %q, want bugfix (always pipeline.name)", conv.Name)
	}
}

// ---------------------------------------------------------------------------
// R2/R3 + publish: DB-backed — converted graph passes validateTemplateGraph
// ---------------------------------------------------------------------------

// TestConvertYAMLToTemplate_Publishable pins the publish contract: a
// converted standard.yaml graph creates a draft + publishes without
// agent binding errors. The default selector names match the seed
// defaults, so the test fixture's standard agents resolve them.
// Requires Postgres; skipped otherwise.
func TestConvertYAMLToTemplate_Publishable(t *testing.T) {
	f := newTestFixture(t)
	// Create the three default-named agents that the converter binds
	// to (workflow-planner / workflow-implementer / workflow-reviewer).
	// They map to the fixture's runtime + workspace so publish resolves
	// them by name.
	f.createAgent(DefaultImportPlannerAgent)
	f.createAgent(DefaultImportImplementerAgent)
	f.createAgent(DefaultImportReviewerAgent)

	p, err := ParsePipelineYAML([]byte(standardYAML))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	conv, err := ConvertYAMLToTemplate(p, "")
	if err != nil {
		t.Fatalf("convert: %v", err)
	}

	detail, err := f.templates.CreateTemplate(context.Background(), CreateTemplateParams{
		WorkspaceID: util.MustParseUUID(f.workspaceID),
		Key:         conv.Key,
		Name:        conv.Name,
		Description: conv.Description,
		CreatedBy:   util.MustParseUUID(f.userID),
		Nodes:       conv.Nodes,
		Edges:       conv.Edges,
	})
	if err != nil {
		t.Fatalf("create template: %v", err)
	}
	if detail.Template.Status != "draft" {
		t.Fatalf("status = %q, want draft", detail.Template.Status)
	}
	// Publish — validateTemplateGraph + resolveAgent + separation must
	// all accept the converted shape.
	published, err := f.templates.PublishTemplate(context.Background(), util.MustParseUUID(f.workspaceID), detail.Template.ID)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if published.Template.Status != "published" {
		t.Fatalf("published status = %q, want published", published.Template.Status)
	}
}

// ---------------------------------------------------------------------------
// AC5: duplicate key rejection (UNIQUE constraint)
// ---------------------------------------------------------------------------

// TestImportYAML_RejectDuplicateKey pins AC5: importing the same key
// twice must fail the second call (workflow_template.key UNIQUE per
// workspace). The error surfaces from CreateTemplate's INSERT — the
// import orchestrator passes it through verbatim.
func TestImportYAML_RejectDuplicateKey(t *testing.T) {
	f := newTestFixture(t)
	ctx := context.Background()

	first, err := ImportYAMLFromBytes(ctx, f.templates, []byte(bugfixYAML), ImportYAMLParams{
		WorkspaceID: f.workspaceID,
		CreatedBy:   f.userID,
		Key:         "bugfix-dup",
	})
	if err != nil {
		t.Fatalf("first import: %v", err)
	}
	if len(first.Nodes) != 6 {
		t.Fatalf("first import nodes = %d, want 6", len(first.Nodes))
	}

	// Second import with the same key — must fail. The exact error
	// string is pgx-specific (unique violation), so we only assert
	// that it IS an error and the second template did not land.
	_, err = ImportYAMLFromBytes(ctx, f.templates, []byte(bugfixYAML), ImportYAMLParams{
		WorkspaceID: f.workspaceID,
		CreatedBy:   f.userID,
		Key:         "bugfix-dup",
	})
	if err == nil {
		t.Fatalf("second import with same key: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "unique") && !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("err = %v, want contains 'unique' or 'duplicate'", err)
	}
	// Only one template row under key=bugfix-dup.
	tmpls, err := f.templates.ListTemplates(ctx, util.MustParseUUID(f.workspaceID))
	if err != nil {
		t.Fatalf("list templates: %v", err)
	}
	dup := 0
	for _, t := range tmpls {
		if t.Key == "bugfix-dup" {
			dup++
		}
	}
	if dup != 1 {
		t.Fatalf("templates with key=bugfix-dup = %d, want exactly 1", dup)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// parseNodeConfigOrFatal finds a node by key in conv.Nodes, parses its
// config, and fails the test on any error.
func parseNodeConfigOrFatal(t *testing.T, nodes []NodeInput, key string) NodeConfig {
	t.Helper()
	for _, n := range nodes {
		if n.NodeKey != key {
			continue
		}
		cfg, err := ParseNodeConfig(n.Config)
		if err != nil {
			t.Fatalf("parse node %q config: %v", key, err)
		}
		return cfg
	}
	t.Fatalf("node %q not found in conversion output", key)
	return NodeConfig{}
}

// nodeKeys extracts the ordered node_key slice from a NodeInput list
// (used in failure messages for readability).
func nodeKeys(nodes []NodeInput) []string {
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.NodeKey)
	}
	return out
}

// compile-time guard: the workflow package tests reference errors.As to
// keep the import list honest when the duplicate-key test expands to
// assert the error type.
var _ = errors.As
