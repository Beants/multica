package handler

// workflow_yaml_import_e2e_test.go — P1-8 harness YAML import
// acceptance-criteria suite (PRD AC1-AC5). One test per AC, exercising
// the full path: ParsePipelineYAML → ConvertYAMLToTemplate → bind
// fixture agents → TemplateService.CreateTemplate (+ PublishTemplate +
// Engine.StartRun where the AC requires it).
//
// These tests are DB-backed via the shared TestMain fixture (testPool +
// testHandler.Queries + testWorkspaceID + testUserID). They skip when
// Postgres is unreachable, matching the rest of the handler test
// contract. Each test creates its own fixture agents + template rows
// and cleans them up via t.Cleanup.
//
// Why handler package, no HTTP: the import path has no HTTP endpoint by
// design (PRD P1-8 / R5 — the CLI talks to the DB directly). The
// handler package is the right home because that is where the rest of
// the workflow AC e2e tests live and where the testPool/testWorkspaceID
// fixture is wired; placing these tests in workflow/ would duplicate
// the fixture wiring.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/workflow"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// yamlImportFixture bundles the per-test resources an AC test needs:
// unique agents, a TemplateService, and the workspace/user identities
// inherited from TestMain. Tests call setupYAMLImportFixture once and
// use the returned helpers to drive the import + publish + run path.
type yamlImportFixture struct {
	t           *testing.T
	templates   *workflow.TemplateService
	executorID  string
	evaluatorID string
	executorNm  string
	evaluatorNm string
	suffix      int64
}

func setupYAMLImportFixture(t *testing.T) *yamlImportFixture {
	t.Helper()
	if testPool == nil {
		t.Skip("database unavailable")
	}
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	f := &yamlImportFixture{
		t:          t,
		templates:  workflow.NewTemplateService(testHandler.Queries, testPool),
		suffix:     suffix,
		executorNm: fmt.Sprintf("YAML Imp Executor %d", suffix),
		evaluatorNm: fmt.Sprintf("YAML Imp Evaluator %d", suffix),
	}
	mkAgent := func(name string) string {
		var id string
		if err := testPool.QueryRow(ctx, `
			INSERT INTO agent (
				workspace_id, name, description, runtime_mode, runtime_config,
				runtime_id, visibility, max_concurrent_tasks, owner_id
			) VALUES ($1, $2, '', 'cloud', '{}'::jsonb, $3, 'private', 1, $4)
			RETURNING id
		`, testWorkspaceID, name, testRuntimeID, testUserID).Scan(&id); err != nil {
			t.Fatalf("create agent %s: %v", name, err)
		}
		return id
	}
	f.executorID = mkAgent(f.executorNm)
	f.evaluatorID = mkAgent(f.evaluatorNm)
	t.Cleanup(func() {
		// Order: tasks → runs → templates → inbox → agents. Workflow
		// FKs are RESTRICT on agent / template, so clean runs+tasks
		// first.
		ctx := context.Background()
		testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE agent_id IN ($1, $2)`, f.executorID, f.evaluatorID)
		testPool.Exec(ctx, `DELETE FROM workflow_run WHERE workspace_id = $1 AND template_id IN (SELECT id FROM workflow_template WHERE workspace_id = $1 AND key LIKE 'yamlimp-%%')`, testWorkspaceID)
		testPool.Exec(ctx, `DELETE FROM workflow_template WHERE workspace_id = $1 AND key LIKE 'yamlimp-%%'`, testWorkspaceID)
		testPool.Exec(ctx, `DELETE FROM inbox_item WHERE workspace_id = $1 AND type LIKE 'workflow_%%'`, testWorkspaceID)
		testPool.Exec(ctx, `DELETE FROM agent WHERE id IN ($1, $2)`, f.executorID, f.evaluatorID)
	})
	return f
}

// importAndBind parses + converts + creates the draft template, binding
// the freshly-created fixture agents via ImportAgentSelectors so the
// converter writes the test agent names into node configs directly
// (rather than the default workflow-* names that would not resolve in
// the shared handler test workspace). Returns the created detail. key
// is namespaced with "yamlimp-" for cleanup.
func (f *yamlImportFixture) importAndBind(yaml string, key string) *workflow.TemplateDetail {
	f.t.Helper()
	ctx := context.Background()
	parsed, err := workflow.ParsePipelineYAML([]byte(yaml))
	if err != nil {
		f.t.Fatalf("parse: %v", err)
	}
	conv, err := workflow.ConvertYAMLToTemplateWithSelectors(parsed, "", workflow.ImportAgentSelectors{
		Planner:     f.executorNm,
		Implementer: f.executorNm,
		Reviewer:    f.evaluatorNm,
	})
	if err != nil {
		f.t.Fatalf("convert: %v", err)
	}
	fullKey := "yamlimp-" + key + "-" + fmt.Sprintf("%d", f.suffix)
	detail, err := f.templates.CreateTemplate(ctx, workflow.CreateTemplateParams{
		WorkspaceID: util.MustParseUUID(testWorkspaceID),
		Key:         fullKey,
		Name:        conv.Name,
		Description: conv.Description,
		CreatedBy:   util.MustParseUUID(testUserID),
		Nodes:       conv.Nodes,
		Edges:       conv.Edges,
	})
	if err != nil {
		f.t.Fatalf("create template: %v", err)
	}
	return detail
}

// publish publishes the given draft, fataling on error.
func (f *yamlImportFixture) publish(detail *workflow.TemplateDetail) *workflow.TemplateDetail {
	f.t.Helper()
	published, err := f.templates.PublishTemplate(context.Background(), util.MustParseUUID(testWorkspaceID), detail.Template.ID)
	if err != nil {
		f.t.Fatalf("publish template: %v", err)
	}
	return published
}

// ---------------------------------------------------------------------------
// AC1: standard.yaml import (8 nodes; PRD AC1 says 9 — see note below)
// ---------------------------------------------------------------------------

func TestAC1StandardImport(t *testing.T) {
	f := setupYAMLImportFixture(t)
	detail := f.importAndBind(standardYAMLLiteral, "standard")

	if detail.Template.Status != "draft" {
		t.Fatalf("status = %q, want draft", detail.Template.Status)
	}
	if got, want := len(detail.Nodes), 8; got != want {
		t.Fatalf("standard nodes = %d, want %d (chain: %v)", got, want, nodeKeysOfWorkflowNode(detail.Nodes))
	}
	// PRD AC1 reads "9 节点" but that count was carried over from
	// seed.go's hand-crafted standard chain (which adds an api-gate
	// stage that does NOT exist in standard.yaml). The YAML-driven
	// import faithfully reflects the YAML's 6 stages + 1 inserted
	// Spec Freeze + terminal end = 8 nodes. Asserting 8 pins the
	// actual contract; future eyes should re-read this comment before
	// "fixing" the count back to 9.
	wantChain := []struct{ key, typ string}{
		{"plan", workflow.NodeTypeAgent},
		{"spec-freeze", workflow.NodeTypeAcceptance},
		{"plan-gate", workflow.NodeTypeGate},
		{"implement", workflow.NodeTypeAgent},
		{"baseline-gate", workflow.NodeTypeGate},
		{"review", workflow.NodeTypeAgent},
		{"acceptance", workflow.NodeTypeAcceptance},
		{"end", workflow.NodeTypeEnd},
	}
	gotByKey := map[string]db.WorkflowNode{}
	for _, n := range detail.Nodes {
		gotByKey[n.NodeKey] = n
	}
	for _, want := range wantChain {
		n, ok := gotByKey[want.key]
		if !ok {
			t.Fatalf("node %q missing from imported template", want.key)
		}
		if n.Type != want.typ {
			t.Fatalf("node %q type = %s, want %s", want.key, n.Type, want.typ)
		}
	}
	if len(detail.Edges) != len(wantChain)-1 {
		t.Fatalf("edges = %d, want %d (linear chain)", len(detail.Edges), len(wantChain)-1)
	}
}

// ---------------------------------------------------------------------------
// AC2: bugfix.yaml import (6 nodes — matches PRD AC2)
// ---------------------------------------------------------------------------

func TestAC2BugfixImport(t *testing.T) {
	f := setupYAMLImportFixture(t)
	detail := f.importAndBind(bugfixYAMLLiteral, "bugfix")

	if got, want := len(detail.Nodes), 6; got != want {
		t.Fatalf("bugfix nodes = %d, want %d (chain: %v)", got, want, nodeKeysOfWorkflowNode(detail.Nodes))
	}
	// 6-node chain: plan-lite → implement-baseline → implement-baseline-gate
	// (split out from stage 2's gate.type=script) → review-soft →
	// acceptance → end.
	wantChain := []struct{ key, typ string}{
		{"plan-lite", workflow.NodeTypeAgent},
		{"implement-baseline", workflow.NodeTypeAgent},
		{"implement-baseline-gate", workflow.NodeTypeGate},
		{"review-soft", workflow.NodeTypeAgent},
		{"acceptance", workflow.NodeTypeAcceptance},
		{"end", workflow.NodeTypeEnd},
	}
	gotByKey := map[string]db.WorkflowNode{}
	for _, n := range detail.Nodes {
		gotByKey[n.NodeKey] = n
	}
	for _, want := range wantChain {
		n, ok := gotByKey[want.key]
		if !ok {
			t.Fatalf("node %q missing", want.key)
		}
		if n.Type != want.typ {
			t.Fatalf("node %q type = %s, want %s", want.key, n.Type, want.typ)
		}
	}
}

// ---------------------------------------------------------------------------
// AC3: imported template can be published (validateTemplateGraph passes)
// ---------------------------------------------------------------------------

func TestAC3Publishable(t *testing.T) {
	f := setupYAMLImportFixture(t)
	for _, tc := range []struct {
		name string
		yaml string
		key  string
	}{
		{"standard", standardYAMLLiteral, "std"},
		{"bugfix", bugfixYAMLLiteral, "bug"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			detail := f.importAndBind(tc.yaml, tc.key)
			published := f.publish(detail)
			if published.Template.Status != "published" {
				t.Fatalf("status = %q, want published", published.Template.Status)
			}
			// Every node has its agent_id frozen at publish (gates
			// have none — they're script-driven).
			for _, n := range published.Nodes {
				cfg, err := workflow.ParseNodeConfig(n.Config)
				if err != nil {
					t.Fatalf("parse node %q config: %v", n.NodeKey, err)
				}
				if n.Type != workflow.NodeTypeAgent {
					continue
				}
				if cfg.AgentID == "" {
					t.Fatalf("agent node %q agent_id not frozen at publish", n.NodeKey)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// AC4: imported template can trigger a run (smoke)
// ---------------------------------------------------------------------------

func TestAC4ImportedTemplateCanRun(t *testing.T) {
	f := setupYAMLImportFixture(t)
	detail := f.importAndBind(standardYAMLLiteral, "std-run")
	published := f.publish(detail)

	// Build an Engine over the test pool and start a run.
	engine := workflow.NewEngine(testHandler.Queries, testPool, testHandler.IssueService, testHandler.TaskService, events.New())
	ctx := context.Background()
	run, created, err := engine.StartRun(ctx, workflow.StartRunParams{
		WorkspaceID: util.MustParseUUID(testWorkspaceID),
		TemplateID:  published.Template.ID,
		SourceType:  "hook",
		SourceID:    fmt.Sprintf("yamlimp-run-%d", f.suffix),
		Title:       "YAML Import AC4 smoke",
		InitiatorID: util.MustParseUUID(testUserID),
	})
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	if !created {
		t.Fatalf("start run: expected created=true")
	}
	if run.Status != workflow.RunRunning {
		t.Fatalf("run status = %q, want running", run.Status)
	}
	// The first agent node (plan) should activate + dispatch a task.
	planStep := latestStepForRun(ctx, t, run.ID, "plan")
	if planStep.Status != workflow.StepActive &&
		planStep.Status != workflow.StepDispatched &&
		planStep.Status != workflow.StepRunning {
		t.Fatalf("plan step = %q, want active/dispatched/running", planStep.Status)
	}
	if !planStep.AgentTaskID.Valid {
		t.Fatalf("plan step has no agent_task_id (activation did not dispatch)")
	}
}

// ---------------------------------------------------------------------------
// AC5: duplicate-key rejection (UNIQUE template key)
// ---------------------------------------------------------------------------

func TestAC5RejectDuplicateKey(t *testing.T) {
	f := setupYAMLImportFixture(t)
	ctx := context.Background()

	// Use the same key twice — the second CreateTemplate must fail.
	// The CLI's ImportYAMLFromBytes wraps CreateTemplate, so the
	// underlying UNIQUE violation surfaces verbatim.
	dupKey := fmt.Sprintf("yamlimp-dup-%d", f.suffix)

	first, err := workflow.ImportYAMLFromBytes(ctx, f.templates, []byte(bugfixYAMLLiteral), workflow.ImportYAMLParams{
		WorkspaceID: testWorkspaceID,
		CreatedBy:   testUserID,
		Key:         dupKey,
	})
	if err != nil {
		t.Fatalf("first import: %v", err)
	}
	if len(first.Nodes) != 6 {
		t.Fatalf("first import nodes = %d, want 6", len(first.Nodes))
	}

	_, err = workflow.ImportYAMLFromBytes(ctx, f.templates, []byte(bugfixYAMLLiteral), workflow.ImportYAMLParams{
		WorkspaceID: testWorkspaceID,
		CreatedBy:   testUserID,
		Key:         dupKey,
	})
	if err == nil {
		t.Fatalf("second import with same key: expected error, got nil")
	}
	// Postgres surfaces this as a unique-violation; assert the
	// string-form so a future driver change doesn't silently weaken
	// the test.
	if !strings.Contains(err.Error(), "unique") && !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("err = %v, want contains 'unique' or 'duplicate'", err)
	}
	// Exactly one row under dupKey.
	var n int
	if err := testPool.QueryRow(ctx,
		`SELECT count(*) FROM workflow_template WHERE workspace_id = $1 AND key = $2`,
		testWorkspaceID, dupKey).Scan(&n); err != nil {
		t.Fatalf("count templates: %v", err)
	}
	if n != 1 {
		t.Fatalf("templates with key=%s = %d, want 1", dupKey, n)
	}
}

// ---------------------------------------------------------------------------
// helpers + YAML literals (mirror harness/pipeline/{standard,bugfix}.yaml)
// ---------------------------------------------------------------------------

func latestStepForRun(ctx context.Context, t *testing.T, runID pgtype.UUID, nodeKey string) db.StepInstance {
	t.Helper()
	step, err := testHandler.Queries.GetLatestStepInstanceForNode(ctx, db.GetLatestStepInstanceForNodeParams{
		RunID: runID, NodeKey: nodeKey,
	})
	if err != nil {
		t.Fatalf("latest step for %q: %v", nodeKey, err)
	}
	return step
}

// nodeKeysOfWorkflowNode extracts the ordered node_key slice for
// failure messages.
func nodeKeysOfWorkflowNode(nodes []db.WorkflowNode) []string {
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.NodeKey)
	}
	return out
}

const standardYAMLLiteral = `# Standard Pipeline — 6 stages, 2 human gates
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

const bugfixYAMLLiteral = `# Bugfix Pipeline — 4 stages, human gate optional
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
