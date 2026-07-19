package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/workflow"
)

// workflow_e2e_ac_test.go — the Wave-3 acceptance-criteria suite (implement.md
// 3.4), one test per AC over the real HTTP handler surface with role-mocked
// agents (executor submissions, evaluator verdicts, member acceptance
// decisions). Mapping:
//
//	AC1 → TestWorkflowHookToCompletionChain (workflow_e2e_test.go, kept)
//	AC2 → TestAC2AcceptanceRejectReworkReexecutesGates
//	AC3 → TestAC3SubmissionMissingExitFieldsStructuredError
//	AC4 → TestAC4RunDetailTraceability
//	AC7 → TestAC7SeedTemplatesInstantiateAndRun
//	AC8 → TestAC8HookContractReplayAndReviewer400
//	AC9 → TestAC9VerdictActorModelAndStepContext
//	AC5/AC6 are covered by make check + the flag-off regression tests
//	(workflow_gate_test.go), not duplicated here.

// ---------------------------------------------------------------------------
// Shared drivers
// ---------------------------------------------------------------------------

// postSubmission records an executor submission through the HTTP surface and
// asserts 201.
func postSubmission(t *testing.T, wh *WorkflowHandler, taskID, agentID, status string, exitFields map[string]any) {
	t.Helper()
	w := httptest.NewRecorder()
	wh.CreateSubmission(w, matRequest(t, "POST", "/api/tasks/"+taskID+"/submission", taskID, agentID, map[string]any{
		"status":      status,
		"exit_fields": exitFields,
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("submission on %s = %d, want 201; body=%s", taskID, w.Code, w.Body.String())
	}
}

// postVerdict records an evaluator pass verdict through the HTTP surface and
// asserts 201.
func postVerdict(t *testing.T, wh *WorkflowHandler, taskID, agentID string, exitFields map[string]any) {
	t.Helper()
	w := httptest.NewRecorder()
	wh.CreateVerdict(w, matRequest(t, "POST", "/api/tasks/"+taskID+"/verdict", taskID, agentID, map[string]any{
		"result":      "pass",
		"exit_fields": exitFields,
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("verdict on %s = %d, want 201; body=%s", taskID, w.Code, w.Body.String())
	}
}

// approveRunAcceptance approves the run's pending acceptance as the member.
func approveRunAcceptance(t *testing.T, wrh *WorkflowRunHandler, runID string) {
	t.Helper()
	w := httptest.NewRecorder()
	wrh.ApproveAcceptance(w, runRequest(t, "POST", "/api/workflow-runs/"+runID+"/acceptance/approve", runID, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("approve = %d, want 200; body=%s", w.Code, w.Body.String())
	}
}

// runStatusForRun re-reads the run status.
func runStatusForRun(t *testing.T, runID string) string {
	t.Helper()
	var status string
	if err := testPool.QueryRow(context.Background(),
		`SELECT status FROM workflow_run WHERE id = $1`, runID).Scan(&status); err != nil {
		t.Fatalf("read run status: %v", err)
	}
	return status
}

// stepAgentForRun returns the frozen agent id of a node's latest step.
func stepAgentForRun(t *testing.T, runID, nodeKey string) string {
	t.Helper()
	var agentID string
	if err := testPool.QueryRow(context.Background(), `
		SELECT agent_id::text FROM step_instance
		WHERE run_id = $1 AND node_key = $2 ORDER BY attempt DESC LIMIT 1
	`, runID, nodeKey).Scan(&agentID); err != nil {
		t.Fatalf("step agent for %q: %v", nodeKey, err)
	}
	return agentID
}

// stepStatusAtAttempt returns a node's step status at one specific attempt.
func stepStatusAtAttempt(t *testing.T, runID, nodeKey string, attempt int) string {
	t.Helper()
	var status string
	if err := testPool.QueryRow(context.Background(), `
		SELECT status FROM step_instance
		WHERE run_id = $1 AND node_key = $2 AND attempt = $3
	`, runID, nodeKey, attempt).Scan(&status); err != nil {
		t.Fatalf("step status for %q attempt %d: %v", nodeKey, attempt, err)
	}
	return status
}

// getRunDetail fetches the run detail DTO through the HTTP surface.
func getRunDetail(t *testing.T, wrh *WorkflowRunHandler, runID string) map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	wrh.GetRun(w, runRequest(t, "GET", "/api/workflow-runs/"+runID, runID, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("run detail = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var detail map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode run detail: %v", err)
	}
	return detail
}

// ---------------------------------------------------------------------------
// AC2: reject acceptance → targeted rework, downstream gates re-executed
// ---------------------------------------------------------------------------

// ac2Template: plan(executor, spec_url required) → implement(executor) →
// gate(evaluator) → accept(acceptance) → end.
func ac2Template(t *testing.T) ([]workflow.NodeInput, []workflow.EdgeInput) {
	return []workflow.NodeInput{
			{NodeKey: "plan", Type: workflow.NodeTypeAgent, Name: "plan", Config: agentCfg(t, workflow.NodeConfig{
				Role:         workflow.RoleExecutor,
				Instructions: "Plan the requirement",
				ExitFields:   &workflow.ExitFieldsSchema{Fields: []workflow.ExitFieldSpec{{Name: "spec_url", Type: "string", Required: true}}},
			})},
			{NodeKey: "implement", Type: workflow.NodeTypeAgent, Name: "implement", Config: agentCfg(t, workflow.NodeConfig{Role: workflow.RoleExecutor, Instructions: "Implement it"})},
			{NodeKey: "gate", Type: workflow.NodeTypeAgent, Name: "gate", Config: agentCfg(t, workflow.NodeConfig{Role: workflow.RoleEvaluator, Instructions: "Judge the baseline"})},
			{NodeKey: "accept", Type: workflow.NodeTypeAcceptance, Name: "accept", Config: agentCfg(t, workflow.NodeConfig{})},
			{NodeKey: "end", Type: workflow.NodeTypeEnd, Name: "end", Config: agentCfg(t, workflow.NodeConfig{})},
		}, []workflow.EdgeInput{
			{FromNodeKey: "plan", ToNodeKey: "implement"},
			{FromNodeKey: "implement", ToNodeKey: "gate"},
			{FromNodeKey: "gate", ToNodeKey: "accept"},
			{FromNodeKey: "accept", ToNodeKey: "end"},
		}
}

// TestAC2AcceptanceRejectReworkReexecutesGates drives the full reject loop
// (prd AC2): ① the implement step re-activates as attempt 2 with the
// rework_context visible in the agent's handoff context; ② the previously
// PASSED downstream gate is set to skipped and RE-EXECUTED on advance; ③ the
// flow reaches acceptance again.
func TestAC2AcceptanceRejectReworkReexecutesGates(t *testing.T) {
	nodes, edges := ac2Template(t)
	f := setupWorkflowAPIFixture(t, "ac2-rework", nodes, edges)
	wh, wrh := f.wh, runHandlerFor(f)
	runID := uuidToString(f.run.ID)

	// Drive to the acceptance wait: plan → implement → gate all pass.
	postSubmission(t, wh, f.stepTask(t, "plan"), f.executorID, "DONE", map[string]any{"spec_url": "https://spec.example/ac2"})
	postSubmission(t, wh, f.stepTask(t, "implement"), f.executorID, "DONE", map[string]any{"pr_url": "https://example/pr/1"})
	postVerdict(t, wh, f.stepTask(t, "gate"), f.evaluatorID, nil)
	if got := runStatusForRun(t, runID); got != workflow.RunWaitingAcceptance {
		t.Fatalf("run = %q, want waiting_acceptance", got)
	}

	// The human rejects, targeting the implement node.
	w := httptest.NewRecorder()
	wrh.RejectAcceptance(w, runRequest(t, "POST", "/api/workflow-runs/"+runID+"/acceptance/reject", runID, map[string]any{
		"reject_to_node_key": "implement",
		"reason":             "wrong API shape",
	}))
	if w.Code != http.StatusOK {
		t.Fatalf("reject = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if got := runStatusForRun(t, runID); got != workflow.RunRunning {
		t.Fatalf("run = %q, want running after reject", got)
	}

	// ① Implement re-activated as attempt 2, rework_context in the handoff.
	implTask := f.stepTask(t, "implement")
	var attempt int
	if err := testPool.QueryRow(context.Background(), `
		SELECT attempt FROM step_instance WHERE run_id = $1 AND node_key = 'implement'
		ORDER BY attempt DESC LIMIT 1
	`, runID).Scan(&attempt); err != nil {
		t.Fatalf("read implement attempt: %v", err)
	}
	if attempt != 2 || f.stepStatus(t, "implement") != workflow.StepActive {
		t.Fatalf("implement = %q attempt %d, want active attempt 2", f.stepStatus(t, "implement"), attempt)
	}
	var note string
	if err := testPool.QueryRow(context.Background(),
		`SELECT coalesce(handoff_note, '') FROM agent_task_queue WHERE id = $1`, implTask).Scan(&note); err != nil {
		t.Fatalf("read handoff note: %v", err)
	}
	if !strings.Contains(note, "[rework]") || !strings.Contains(note, "wrong API shape") {
		t.Fatalf("handoff note missing rework context (D-8 explicit injection):\n%s", note)
	}

	// ② Previously-passed downstream gate + the old acceptance/end rows are
	// invalidated to skipped (they must re-run, not be reused).
	if got := stepStatusAtAttempt(t, runID, "gate", 1); got != workflow.StepSkipped {
		t.Fatalf("gate attempt 1 = %q, want skipped (passed gate invalidated)", got)
	}
	if got := stepStatusAtAttempt(t, runID, "accept", 1); got != workflow.StepSkipped {
		t.Fatalf("accept attempt 1 = %q, want skipped", got)
	}
	if got := stepStatusAtAttempt(t, runID, "end", 1); got != workflow.StepSkipped {
		t.Fatalf("end attempt 1 = %q, want skipped", got)
	}
	// The rejected acceptance row carries the rework context into the trace.
	detail := getRunDetail(t, wrh, runID)
	accs, _ := detail["acceptances"].([]any)
	if len(accs) != 1 {
		t.Fatalf("acceptances = %v, want the rejected row", accs)
	}
	acc := accs[0].(map[string]any)
	if acc["status"] != "rejected" || acc["reject_to_node_key"] != "implement" {
		t.Fatalf("acceptance = %v, want rejected → implement", acc)
	}
	if rc, _ := acc["rework_context"].(map[string]any); rc["reason"] != "wrong API shape" {
		t.Fatalf("rework_context = %v, want the reject reason recorded", acc["rework_context"])
	}

	// Re-running implement re-executes the skipped gate at attempt 2.
	postSubmission(t, wh, implTask, f.executorID, "DONE", map[string]any{"pr_url": "https://example/pr/2"})
	if got := f.stepStatus(t, "gate"); got != workflow.StepActive {
		t.Fatalf("gate = %q, want active (re-executed after invalidation)", got)
	}
	var gateAttempt int
	if err := testPool.QueryRow(context.Background(), `
		SELECT attempt FROM step_instance WHERE run_id = $1 AND node_key = 'gate'
		ORDER BY attempt DESC LIMIT 1
	`, runID).Scan(&gateAttempt); err != nil {
		t.Fatalf("read gate attempt: %v", err)
	}
	if gateAttempt != 2 {
		t.Fatalf("gate attempt = %d, want 2 (re-execution)", gateAttempt)
	}

	// ③ The flow reaches acceptance again.
	postVerdict(t, wh, f.stepTask(t, "gate"), f.evaluatorID, nil)
	if got := runStatusForRun(t, runID); got != workflow.RunWaitingAcceptance {
		t.Fatalf("run = %q, want waiting_acceptance (reached acceptance again)", got)
	}
	if _, err := testHandler.Queries.GetPendingAcceptanceByRun(context.Background(), f.run.ID); err != nil {
		t.Fatalf("no fresh pending acceptance after rework loop: %v", err)
	}
	approveRunAcceptance(t, wrh, runID)
	if got := runStatusForRun(t, runID); got != workflow.RunCompleted {
		t.Fatalf("run = %q, want completed", got)
	}
}

// ---------------------------------------------------------------------------
// AC3: missing required exit fields → structured rejection, no verdict
// ---------------------------------------------------------------------------

func TestAC3SubmissionMissingExitFieldsStructuredError(t *testing.T) {
	nodes, edges := twoNodeTemplate(t, true) // work requires pr_url → end
	f := setupWorkflowAPIFixture(t, "ac3-fields", nodes, edges)
	taskID := f.stepTask(t, "work")

	w := httptest.NewRecorder()
	f.wh.CreateSubmission(w, matRequest(t, "POST", "/api/tasks/"+taskID+"/submission", taskID, f.executorID, map[string]any{
		"status":      "DONE",
		"exit_fields": map[string]any{},
	}))
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("submission = %d, want 422; body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Error  string `json:"error"`
		Fields []struct {
			Name     string `json:"name"`
			Code     string `json:"code"`
			Expected string `json:"expected"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode 422 body: %v", err)
	}
	if len(body.Fields) != 1 || body.Fields[0].Name != "pr_url" || body.Fields[0].Code != "missing" || body.Fields[0].Expected != "string" {
		t.Fatalf("structured fields = %+v, want one missing pr_url (string)", body.Fields)
	}

	// No submission, no verdict derived, step and run untouched.
	var subs, verdicts int
	if err := testPool.QueryRow(context.Background(), `
		SELECT
			(SELECT count(*) FROM submission s JOIN step_instance si ON si.id = s.step_instance_id WHERE si.run_id = $1),
			(SELECT count(*) FROM verdict v JOIN step_instance si ON si.id = v.step_instance_id WHERE si.run_id = $1)
	`, f.run.ID).Scan(&subs, &verdicts); err != nil {
		t.Fatalf("count submission/verdict: %v", err)
	}
	if subs != 0 || verdicts != 0 {
		t.Fatalf("submissions=%d verdicts=%d, want 0/0 (rejected before verdict derivation)", subs, verdicts)
	}
	if got := f.stepStatus(t, "work"); got != workflow.StepActive {
		t.Fatalf("work step = %q, want still active", got)
	}
	if got := runStatusForRun(t, uuidToString(f.run.ID)); got != workflow.RunRunning {
		t.Fatalf("run = %q, want still running", got)
	}
}

// ---------------------------------------------------------------------------
// AC4: every step's issue/task/submission/verdict/transition traceable
// ---------------------------------------------------------------------------

func TestAC4RunDetailTraceability(t *testing.T) {
	nodes, edges := e2eChainTemplate(t) // work → gate → review(acceptance) → end
	f := setupWorkflowAPIFixture(t, "ac4-trace", nodes, edges)
	wh, wrh := f.wh, runHandlerFor(f)
	runID := uuidToString(f.run.ID)

	postSubmission(t, wh, f.stepTask(t, "work"), f.executorID, "DONE", map[string]any{"pr_url": "https://example/pr/1"})
	postVerdict(t, wh, f.stepTask(t, "gate"), f.evaluatorID, nil)
	approveRunAcceptance(t, wrh, runID)
	if got := runStatusForRun(t, runID); got != workflow.RunCompleted {
		t.Fatalf("run = %q, want completed", got)
	}

	detail := getRunDetail(t, wrh, runID)

	// Steps: agent steps carry issue + task links; acceptance/end steps do
	// not dispatch (no child issue) but remain first-class timeline rows.
	steps, _ := detail["steps"].([]any)
	if len(steps) != 4 {
		t.Fatalf("steps = %d, want 4", len(steps))
	}
	byNode := map[string]map[string]any{}
	for _, s := range steps {
		sm := s.(map[string]any)
		byNode[sm["node_key"].(string)] = sm
	}
	for _, key := range []string{"work", "gate"} {
		if byNode[key]["issue_id"] == nil || byNode[key]["agent_task_id"] == nil {
			t.Fatalf("agent step %q missing issue/task links: %v", key, byNode[key])
		}
		if byNode[key]["status"] != workflow.StepPassed {
			t.Fatalf("step %q = %v, want passed", key, byNode[key]["status"])
		}
	}
	for _, key := range []string{"review", "end"} {
		if byNode[key]["issue_id"] != nil || byNode[key]["agent_task_id"] != nil {
			t.Fatalf("non-dispatch step %q should not carry issue/task links: %v", key, byNode[key])
		}
	}
	// The node child issues hang under the intake parent issue.
	var childCount int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM issue WHERE parent_issue_id = $1
	`, f.run.IntakeIssueID).Scan(&childCount); err != nil {
		t.Fatalf("count child issues: %v", err)
	}
	if childCount != 2 {
		t.Fatalf("child issues = %d, want 2 (work + gate under intake %s)", childCount, runID)
	}

	// Submissions: the executor's own plus the auto-created minimal one the
	// evaluator verdict hangs on (design.md §4.3), each bound to its step.
	subs, _ := detail["submissions"].([]any)
	if len(subs) != 2 {
		t.Fatalf("submissions = %d, want 2 (work + gate)", len(subs))
	}
	stepIDByNode := map[string]string{}
	for key, sm := range byNode {
		stepIDByNode[key] = sm["id"].(string)
	}
	seenSub := map[string]bool{}
	for _, s := range subs {
		sm := s.(map[string]any)
		seenSub[sm["step_instance_id"].(string)] = true
	}
	for _, key := range []string{"work", "gate"} {
		if !seenSub[stepIDByNode[key]] {
			t.Fatalf("no submission bound to %s step %s", key, stepIDByNode[key])
		}
	}

	// Verdicts: one per agent step, actors per the verdict actor model.
	verdicts, _ := detail["verdicts"].([]any)
	if len(verdicts) != 2 {
		t.Fatalf("verdicts = %d, want 2", len(verdicts))
	}
	verdictByByStep := map[string]string{}
	for _, v := range verdicts {
		vm := v.(map[string]any)
		verdictByByStep[vm["step_instance_id"].(string)] = vm["verdict_by"].(string)
	}
	if verdictByByStep[stepIDByNode["work"]] != "system" || verdictByByStep[stepIDByNode["gate"]] != "agent" {
		t.Fatalf("verdict actors = %v, want work=system gate=agent", verdictByByStep)
	}

	// Acceptance decided by the current member.
	accs, _ := detail["acceptances"].([]any)
	if len(accs) != 1 {
		t.Fatalf("acceptances = %d, want 1", len(accs))
	}
	acc := accs[0].(map[string]any)
	if acc["status"] != "approved" || acc["step_instance_id"] != stepIDByNode["review"] {
		t.Fatalf("acceptance = %v, want approved on the review step", acc)
	}
	if acc["reviewer_id"] != currentMemberID(t) || acc["decided_at"] == nil {
		t.Fatalf("acceptance reviewer/decided_at = %v", acc)
	}

	// Transitions: every step's full status chain is recorded.
	transitions, _ := detail["transitions"].([]any)
	if len(transitions) == 0 {
		t.Fatalf("transitions empty")
	}
	trByStep := map[string]int{}
	for _, tr := range transitions {
		tm := tr.(map[string]any)
		trByStep[tm["step_instance_id"].(string)]++
		if tm["from_status"] == tm["to_status"] {
			t.Fatalf("degenerate transition: %v", tm)
		}
	}
	for key, stepID := range stepIDByNode {
		if trByStep[stepID] == 0 {
			t.Fatalf("step %q (%s) has no transitions", key, stepID)
		}
	}

	// The frozen snapshot the steps reference by node_key travels with the run.
	snap, _ := detail["template_snapshot"].(map[string]any)
	snapNodes, _ := snap["nodes"].([]any)
	if len(snapNodes) != 4 {
		t.Fatalf("template_snapshot nodes = %d, want 4", len(snapNodes))
	}
	if detail["intake_issue_id"] == nil {
		t.Fatalf("detail missing intake_issue_id")
	}
}

// ---------------------------------------------------------------------------
// AC7: seed templates instantiate + a full run on each
// ---------------------------------------------------------------------------

// ac7Fixture bundles the four seed agents and the handlers under test.
type ac7Fixture struct {
	wth         *WorkflowTemplateHandler
	wh          *WorkflowHandler
	wrh         *WorkflowRunHandler
	engine      *workflow.Engine
	plannerID   string
	implID      string
	gateID      string
	reviewerID  string
	agentSuffix int64
}

func setupAC7Fixture(t *testing.T) *ac7Fixture {
	t.Helper()
	ctx := context.Background()
	f := &ac7Fixture{agentSuffix: time.Now().UnixNano()}
	mkAgent := func(role string) string {
		var id string
		if err := testPool.QueryRow(ctx, `
			INSERT INTO agent (
				workspace_id, name, description, runtime_mode, runtime_config,
				runtime_id, visibility, max_concurrent_tasks, owner_id
			) VALUES ($1, $2, '', 'cloud', '{}'::jsonb, $3, 'private', 1, $4)
			RETURNING id
		`, testWorkspaceID, fmt.Sprintf("WF Seed %s %d", role, f.agentSuffix), testRuntimeID, testUserID).Scan(&id); err != nil {
			t.Fatalf("create %s agent: %v", role, err)
		}
		return id
	}
	f.plannerID = mkAgent("Planner")
	f.implID = mkAgent("Implementer")
	f.gateID = mkAgent("GateRunner")
	f.reviewerID = mkAgent("Reviewer")

	templates := workflow.NewTemplateService(testHandler.Queries, testPool)
	f.wth = NewWorkflowTemplateHandler(testHandler.Queries, templates)
	f.engine = workflow.NewEngine(testHandler.Queries, testPool, testHandler.IssueService, testHandler.TaskService, events.New())
	f.wh = NewWorkflowHandler(testHandler.Queries, f.engine)
	f.wrh = NewWorkflowRunHandler(testHandler.Queries, f.engine)

	t.Cleanup(func() {
		ctx := context.Background()
		testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE agent_id IN ($1, $2, $3, $4)`, f.plannerID, f.implID, f.gateID, f.reviewerID)
		// workflow_run.template_id is FK RESTRICT: runs (and their issues)
		// go before the template rows.
		testPool.Exec(ctx, `DELETE FROM issue WHERE parent_issue_id IN (SELECT intake_issue_id FROM workflow_run WHERE workspace_id = $1)`, testWorkspaceID)
		testPool.Exec(ctx, `DELETE FROM issue WHERE id IN (SELECT intake_issue_id FROM workflow_run WHERE workspace_id = $1)`, testWorkspaceID)
		testPool.Exec(ctx, `DELETE FROM workflow_run WHERE workspace_id = $1`, testWorkspaceID)
		testPool.Exec(ctx, `DELETE FROM workflow_template WHERE workspace_id = $1 AND key IN ('standard', 'bugfix')`, testWorkspaceID)
		testPool.Exec(ctx, `DELETE FROM inbox_item WHERE workspace_id = $1 AND type LIKE 'workflow_%'`, testWorkspaceID)
		testPool.Exec(ctx, `DELETE FROM agent WHERE id IN ($1, $2, $3, $4)`, f.plannerID, f.implID, f.gateID, f.reviewerID)
	})
	return f
}

// seedViaAPI calls POST /api/workflow-templates/seed as the member.
func seedViaAPI(t *testing.T, f *ac7Fixture) map[string]map[string]any {
	t.Helper()
	agentName := func(id string) string {
		var name string
		if err := testPool.QueryRow(context.Background(), `SELECT name FROM agent WHERE id = $1`, id).Scan(&name); err != nil {
			t.Fatalf("read agent name: %v", err)
		}
		return name
	}
	w := httptest.NewRecorder()
	f.wth.SeedTemplates(w, memberRequest(t, "POST", "/api/workflow-templates/seed", map[string]any{
		"planner_agent":     agentName(f.plannerID),
		"implementer_agent": agentName(f.implID),
		"gate_agent":        agentName(f.gateID),
		"review_agent":      agentName(f.reviewerID),
	}))
	if w.Code != http.StatusOK {
		t.Fatalf("seed = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Templates []struct {
			Key        string `json:"key"`
			TemplateID string `json:"template_id"`
			Version    int32  `json:"version"`
			Seeded     bool   `json:"seeded"`
		} `json:"templates"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode seed response: %v", err)
	}
	out := map[string]map[string]any{}
	for _, tm := range resp.Templates {
		out[tm.Key] = map[string]any{"template_id": tm.TemplateID, "version": tm.Version, "seeded": tm.Seeded}
	}
	return out
}

// assertSeedStructure checks one seeded template's graph through the GET API.
func assertSeedStructure(t *testing.T, f *ac7Fixture, templateID string, wantChain []string, wantTypes map[string]string, wantRoles map[string]string) {
	t.Helper()
	w := httptest.NewRecorder()
	f.wth.GetTemplate(w, withURLParam(memberRequest(t, "GET", "/api/workflow-templates/"+templateID, nil), "id", templateID))
	if w.Code != http.StatusOK {
		t.Fatalf("get template = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var detail struct {
		Status string `json:"status"`
		Nodes  []struct {
			NodeKey string          `json:"node_key"`
			Type    string          `json:"type"`
			Config  json.RawMessage `json:"config"`
		} `json:"nodes"`
		Edges []struct {
			FromNodeKey string `json:"from_node_key"`
			ToNodeKey   string `json:"to_node_key"`
		} `json:"edges"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode template detail: %v", err)
	}
	if detail.Status != "published" {
		t.Fatalf("template status = %q, want published", detail.Status)
	}
	if len(detail.Nodes) != len(wantChain) {
		t.Fatalf("nodes = %d, want %d", len(detail.Nodes), len(wantChain))
	}
	if len(detail.Edges) != len(wantChain)-1 {
		t.Fatalf("edges = %d, want %d (linear chain)", len(detail.Edges), len(wantChain)-1)
	}
	executorAgents := map[string]bool{}
	evaluatorAgents := map[string]bool{}
	for _, n := range detail.Nodes {
		if wantTypes[n.NodeKey] != n.Type {
			t.Fatalf("node %q type = %q, want %q", n.NodeKey, n.Type, wantTypes[n.NodeKey])
		}
		cfg, err := workflow.ParseNodeConfig(n.Config)
		if err != nil {
			t.Fatalf("parse node %q config: %v", n.NodeKey, err)
		}
		if wantRoles[n.NodeKey] != "" && cfg.EffectiveRole() != wantRoles[n.NodeKey] {
			t.Fatalf("node %q role = %q, want %q", n.NodeKey, cfg.EffectiveRole(), wantRoles[n.NodeKey])
		}
		if cfg.EffectiveMaxAttempts() != 3 {
			t.Fatalf("node %q max_attempts = %d, want 3", n.NodeKey, cfg.EffectiveMaxAttempts())
		}
		if cfg.AutoPass {
			t.Fatalf("node %q auto_pass = true, want false (D-12)", n.NodeKey)
		}
		if n.Type == workflow.NodeTypeAgent {
			if cfg.AgentID == "" {
				t.Fatalf("node %q agent_id not frozen at publish", n.NodeKey)
			}
			if cfg.EffectiveRole() == workflow.RoleExecutor {
				executorAgents[cfg.AgentID] = true
			} else {
				evaluatorAgents[cfg.AgentID] = true
			}
		}
	}
	for eval := range evaluatorAgents {
		if executorAgents[eval] {
			t.Fatalf("evaluator agent %s is also an executor agent (separation violation)", eval)
		}
	}
}

// driveSeedRun walks one full run on a seeded template: executor submissions,
// evaluator verdicts, and member approvals at every acceptance node, all
// through the HTTP surface. chain lists the agent/acceptance node keys in
// order; submissions/verdicts carry the seed schema's required exit fields.
func driveSeedRun(t *testing.T, f *ac7Fixture, runID string, chain []string, exitFields map[string]map[string]any) {
	t.Helper()
	for _, nodeKey := range chain {
		latestStatus := stepStatusForRun(t, runID, nodeKey)
		if latestStatus != workflow.StepActive {
			t.Fatalf("node %q = %q, want active before driving it", nodeKey, latestStatus)
		}
		cfg := seedNodeConfigForRun(t, runID, nodeKey)
		switch {
		case cfg.typ == workflow.NodeTypeAgent && cfg.role == workflow.RoleExecutor:
			postSubmission(t, f.wh, stepTaskForRun(t, runID, nodeKey), stepAgentForRun(t, runID, nodeKey), "DONE", exitFields[nodeKey])
		case cfg.typ == workflow.NodeTypeAgent && cfg.role == workflow.RoleEvaluator:
			postVerdict(t, f.wh, stepTaskForRun(t, runID, nodeKey), stepAgentForRun(t, runID, nodeKey), exitFields[nodeKey])
		case cfg.typ == workflow.NodeTypeAcceptance:
			if got := runStatusForRun(t, runID); got != workflow.RunWaitingAcceptance {
				t.Fatalf("run = %q at %q, want waiting_acceptance", got, nodeKey)
			}
			approveRunAcceptance(t, f.wrh, runID)
		}
	}
}

// seedNodeConfigForRun reads a node's type/role from the run's frozen
// snapshot (what the engine itself sees).
func seedNodeConfigForRun(t *testing.T, runID, nodeKey string) (out struct {
	typ  string
	role string
},
) {
	t.Helper()
	var snapRaw []byte
	if err := testPool.QueryRow(context.Background(),
		`SELECT template_snapshot FROM workflow_run WHERE id = $1`, runID).Scan(&snapRaw); err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	snap, err := workflow.ParseSnapshot(snapRaw)
	if err != nil {
		t.Fatalf("parse snapshot: %v", err)
	}
	node := snap.NodeByKey(nodeKey)
	if node == nil {
		t.Fatalf("node %q missing from snapshot", nodeKey)
	}
	out.typ = node.Type
	out.role = node.Config.EffectiveRole()
	return out
}

func TestAC7SeedTemplatesInstantiateAndRun(t *testing.T) {
	f := setupAC7Fixture(t)

	// Seed via the API; both templates must report seeded.
	seeded := seedViaAPI(t, f)
	if len(seeded) != 2 || seeded["standard"]["seeded"] != true || seeded["bugfix"]["seeded"] != true {
		t.Fatalf("seed response = %v, want standard+bugfix seeded", seeded)
	}

	// Structure asserts (AC7 first half).
	assertSeedStructure(t, f, seeded["standard"]["template_id"].(string),
		[]string{"plan", "plan-gate", "spec-freeze", "implement", "baseline-gate", "api-gate", "review", "final-acceptance", "done"},
		map[string]string{
			"plan": workflow.NodeTypeAgent, "plan-gate": workflow.NodeTypeAgent, "spec-freeze": workflow.NodeTypeAcceptance,
			"implement": workflow.NodeTypeAgent, "baseline-gate": workflow.NodeTypeAgent, "api-gate": workflow.NodeTypeAgent,
			"review": workflow.NodeTypeAgent, "final-acceptance": workflow.NodeTypeAcceptance, "done": workflow.NodeTypeEnd,
		},
		map[string]string{
			"plan": workflow.RoleExecutor, "implement": workflow.RoleExecutor,
			"plan-gate": workflow.RoleEvaluator, "baseline-gate": workflow.RoleEvaluator,
			"api-gate": workflow.RoleEvaluator, "review": workflow.RoleEvaluator,
		})
	assertSeedStructure(t, f, seeded["bugfix"]["template_id"].(string),
		[]string{"plan-lite", "implement", "baseline-gate", "review", "final-acceptance", "done"},
		map[string]string{
			"plan-lite": workflow.NodeTypeAgent, "implement": workflow.NodeTypeAgent,
			"baseline-gate": workflow.NodeTypeAgent, "review": workflow.NodeTypeAgent,
			"final-acceptance": workflow.NodeTypeAcceptance, "done": workflow.NodeTypeEnd,
		},
		map[string]string{
			"plan-lite": workflow.RoleExecutor, "implement": workflow.RoleExecutor,
			"baseline-gate": workflow.RoleEvaluator, "review": workflow.RoleEvaluator,
		})

	// Full run on the standard seed (AC7 second half): every stage driven by
	// its role's mock agent, both human gates approved by the member.
	standardRun, created, err := f.engine.StartRun(context.Background(), workflow.StartRunParams{
		WorkspaceID: util.MustParseUUID(testWorkspaceID),
		TemplateID:  util.MustParseUUID(seeded["standard"]["template_id"].(string)),
		SourceType:  "hook",
		SourceID:    fmt.Sprintf("ac7-standard-%d", f.agentSuffix),
		Title:       "AC7 standard seed run",
		InitiatorID: util.MustParseUUID(testUserID),
	})
	if err != nil || !created {
		t.Fatalf("start standard run: %v (created=%v)", err, created)
	}
	standardRunID := uuidToString(standardRun.ID)
	driveSeedRun(t, f, standardRunID,
		[]string{"plan", "plan-gate", "spec-freeze", "implement", "baseline-gate", "api-gate", "review", "final-acceptance"},
		map[string]map[string]any{
			"plan":          {"prd_url": "https://docs.example/prd", "design_url": "https://docs.example/design", "business_test_cases_url": "https://docs.example/btc"},
			"plan-gate":     {"gate_report_url": "https://gate.example/plan"},
			"implement":     {"pr_url": "https://example/pr/9", "branch": "feat/ac7", "summary": "implemented"},
			"baseline-gate": {"baseline_diff_url": "https://gate.example/baseline-diff"},
			"api-gate":      {"gate_report_url": "https://gate.example/api"},
			"review":        {"decision": "APPROVED"},
		})
	if got := runStatusForRun(t, standardRunID); got != workflow.RunCompleted {
		t.Fatalf("standard run = %q, want completed", got)
	}
	for _, key := range []string{"plan", "plan-gate", "spec-freeze", "implement", "baseline-gate", "api-gate", "review", "final-acceptance", "done"} {
		if got := stepStatusForRun(t, standardRunID, key); got != workflow.StepPassed {
			t.Fatalf("standard step %q = %q, want passed", key, got)
		}
	}

	// Full run on the bugfix seed.
	bugfixRun, created, err := f.engine.StartRun(context.Background(), workflow.StartRunParams{
		WorkspaceID: util.MustParseUUID(testWorkspaceID),
		TemplateID:  util.MustParseUUID(seeded["bugfix"]["template_id"].(string)),
		SourceType:  "hook",
		SourceID:    fmt.Sprintf("ac7-bugfix-%d", f.agentSuffix),
		Title:       "AC7 bugfix seed run",
		InitiatorID: util.MustParseUUID(testUserID),
	})
	if err != nil || !created {
		t.Fatalf("start bugfix run: %v (created=%v)", err, created)
	}
	bugfixRunID := uuidToString(bugfixRun.ID)
	driveSeedRun(t, f, bugfixRunID,
		[]string{"plan-lite", "implement", "baseline-gate", "review", "final-acceptance"},
		map[string]map[string]any{
			"plan-lite":     {"prd_url": "https://docs.example/bug-prd", "business_test_cases_url": "https://docs.example/bug-btc"},
			"implement":     {"pr_url": "https://example/pr/10", "branch": "fix/ac7", "summary": "fixed"},
			"baseline-gate": {"baseline_diff_url": "https://gate.example/bug-diff"},
			"review":        {"decision": "APPROVED"},
		})
	if got := runStatusForRun(t, bugfixRunID); got != workflow.RunCompleted {
		t.Fatalf("bugfix run = %q, want completed", got)
	}
	for _, key := range []string{"plan-lite", "implement", "baseline-gate", "review", "final-acceptance", "done"} {
		if got := stepStatusForRun(t, bugfixRunID, key); got != workflow.StepPassed {
			t.Fatalf("bugfix step %q = %q, want passed", key, got)
		}
	}

	// Idempotency through the same API: a second seed skips both keys.
	reseed := seedViaAPI(t, f)
	for key, r := range reseed {
		if r["seeded"] != false {
			t.Fatalf("re-seed %q = %v, want seeded=false (idempotent skip)", key, r)
		}
	}
}

// ---------------------------------------------------------------------------
// AC8: hook contract — idempotent replay + reviewer validation
// ---------------------------------------------------------------------------

func TestAC8HookContractReplayAndReviewer400(t *testing.T) {
	nodes, edges := twoNodeTemplate(t, false)
	f := setupHookFixture(t, "ac8-hook", nodes, edges)

	// ① Replay: the same source_id pushed twice returns 200 + the ORIGINAL
	// run_id and creates nothing new.
	sourceID := fmt.Sprintf("ac8-src-%d", time.Now().UnixNano())
	payload := map[string]any{"title": "AC8 work item", "source_id": sourceID}
	w := httptest.NewRecorder()
	f.hh.HandleInboundHook(w, hookRequest(t, f.token, payload))
	if w.Code != http.StatusCreated {
		t.Fatalf("first push = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	first := decodeHookResponse(t, w)

	w = httptest.NewRecorder()
	f.hh.HandleInboundHook(w, hookRequest(t, f.token, payload))
	if w.Code != http.StatusOK {
		t.Fatalf("replay = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	second := decodeHookResponse(t, w)
	if second["run_id"] != first["run_id"] || second["created"] != false {
		t.Fatalf("replay = %v, want same run_id + created=false", second)
	}
	if n := countRunsForSource(t, sourceID); n != 1 {
		t.Fatalf("runs for source = %d, want exactly 1 (no duplicate)", n)
	}

	// ② Reviewer that is neither a member id nor a member email → 400.
	for name, reviewer := range map[string]string{
		"unknown email":   "nobody@example.com",
		"unknown uuid":    "00000000-0000-0000-0000-000000000099",
		"malformed value": "not-an-id-not-an-email",
	} {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			f.hh.HandleInboundHook(w, hookRequest(t, f.token, map[string]any{
				"title":     "AC8 bad reviewer",
				"source_id": fmt.Sprintf("ac8-bad-%d", time.Now().UnixNano()),
				"reviewer":  reviewer,
			}))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("push with %s reviewer = %d, want 400; body=%s", name, w.Code, w.Body.String())
			}
		})
	}
}

// ---------------------------------------------------------------------------
// AC9: verdict actor model + step context contract
// ---------------------------------------------------------------------------

func TestAC9VerdictActorModelAndStepContext(t *testing.T) {
	nodes, edges := threeNodeTemplate(t, true) // work(executor) → gate(evaluator, verdict_score required) → end
	f := setupWorkflowAPIFixture(t, "ac9-actors", nodes, edges)
	runID := uuidToString(f.run.ID)

	// ① Executor token writing a verdict → 403 (its steps are judged by the
	// system-derived verdict).
	workTask := f.stepTask(t, "work")
	w := httptest.NewRecorder()
	f.wh.CreateVerdict(w, matRequest(t, "POST", "/api/tasks/"+workTask+"/verdict", workTask, f.executorID, map[string]any{
		"result": "pass",
	}))
	if w.Code != http.StatusForbidden {
		t.Fatalf("executor verdict = %d, want 403; body=%s", w.Code, w.Body.String())
	}

	// Drive the executor step; its exit fields become the gate's upstream
	// context (unknown-schema fields pass through, D-9).
	postSubmission(t, f.wh, workTask, f.executorID, "DONE", map[string]any{
		"pr_url": "https://example/pr/9",
		"branch": "feat/ac9",
	})

	// ③ step context: instructions + upstream exit fields + this node's
	// exit-fields schema.
	gateTask := f.stepTask(t, "gate")
	w = httptest.NewRecorder()
	f.wh.GetStepContext(w, matRequest(t, "GET", "/api/tasks/"+gateTask+"/step-context", gateTask, f.evaluatorID, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("step context = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var sc struct {
		NodeKey          string         `json:"node_key"`
		Role             string         `json:"role"`
		Instructions     string         `json:"instructions"`
		UpstreamNodeKey  string         `json:"upstream_node_key"`
		UpstreamFields   map[string]any `json:"upstream_exit_fields"`
		ExitFieldsSchema struct {
			Fields []struct {
				Name     string `json:"name"`
				Type     string `json:"type"`
				Required bool   `json:"required"`
			} `json:"fields"`
		} `json:"exit_fields_schema"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &sc); err != nil {
		t.Fatalf("decode step context: %v", err)
	}
	if sc.NodeKey != "gate" || sc.Role != workflow.RoleEvaluator {
		t.Fatalf("step context identity = %q/%q, want gate/evaluator", sc.NodeKey, sc.Role)
	}
	if sc.Instructions != "Judge" {
		t.Fatalf("instructions = %q, want the node instructions", sc.Instructions)
	}
	if sc.UpstreamNodeKey != "work" || sc.UpstreamFields["pr_url"] != "https://example/pr/9" {
		t.Fatalf("upstream context = %q %v, want work + pr_url", sc.UpstreamNodeKey, sc.UpstreamFields)
	}
	if len(sc.ExitFieldsSchema.Fields) != 1 || sc.ExitFieldsSchema.Fields[0].Name != "verdict_score" || !sc.ExitFieldsSchema.Fields[0].Required {
		t.Fatalf("exit schema = %+v, want required verdict_score", sc.ExitFieldsSchema.Fields)
	}

	// ② Evaluator token writes the verdict → 201 (carrying the required exit
	// field for the auto-created minimal submission).
	postVerdict(t, f.wh, gateTask, f.evaluatorID, map[string]any{"verdict_score": 0.9})
	var result, by string
	if err := testPool.QueryRow(context.Background(), `
		SELECT v.result, v.verdict_by FROM verdict v
		JOIN step_instance si ON si.id = v.step_instance_id
		WHERE si.run_id = $1 AND si.node_key = 'gate'
	`, runID).Scan(&result, &by); err != nil {
		t.Fatalf("read gate verdict: %v", err)
	}
	if result != "pass" || by != "agent" {
		t.Fatalf("gate verdict = %q by %q, want pass by agent", result, by)
	}
}
