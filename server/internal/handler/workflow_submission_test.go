package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/workflow"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// workflow_submission_test.go — API contract tests for the agent-facing
// workflow endpoints. All tests need the DB (TestMain wires testHandler /
// testPool / testWorkspaceID / testRuntimeID and skips when Postgres is
// unreachable). Requests carry the mat_-stamped headers directly — the auth
// middleware itself is upstream and out of scope; what these tests assert is
// the handler's own trust rules (task-token-only, URL==token binding).

// workflowAPIFixture bundles a published template + started run + the
// handler under test.
type workflowAPIFixture struct {
	wh          *WorkflowHandler
	engine      *workflow.Engine
	executorID  string
	evaluatorID string
	templateID  pgtype.UUID
	run         db.WorkflowRun
	intakeIssue pgtype.UUID
}

func (f *workflowAPIFixture) cleanup(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE agent_id IN ($1, $2)`, f.executorID, f.evaluatorID)
	if f.run.ID.Valid {
		testPool.Exec(ctx, `DELETE FROM workflow_run WHERE id = $1`, f.run.ID)
	}
	if f.templateID.Valid {
		testPool.Exec(ctx, `DELETE FROM workflow_template WHERE id = $1`, f.templateID)
	}
	if f.intakeIssue.Valid {
		testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1 OR parent_issue_id = $1`, f.intakeIssue)
	}
	testPool.Exec(ctx, `DELETE FROM inbox_item WHERE workspace_id = $1 AND type LIKE 'workflow_%'`, testWorkspaceID)
	testPool.Exec(ctx, `DELETE FROM agent WHERE id IN ($1, $2)`, f.executorID, f.evaluatorID)
}

// setupWorkflowAPIFixture creates executor/evaluator agents (unique names —
// selector resolution by name must stay unambiguous in the shared test
// workspace), publishes the given template, and starts a run.
func setupWorkflowAPIFixture(t *testing.T, key string, nodes []workflow.NodeInput, edges []workflow.EdgeInput) *workflowAPIFixture {
	t.Helper()
	ctx := context.Background()
	suffix := time.Now().UnixNano()

	f := &workflowAPIFixture{}
	mkAgent := func(role string) string {
		var id string
		if err := testPool.QueryRow(ctx, `
			INSERT INTO agent (
				workspace_id, name, description, runtime_mode, runtime_config,
				runtime_id, visibility, max_concurrent_tasks, owner_id
			) VALUES ($1, $2, '', 'cloud', '{}'::jsonb, $3, 'private', 1, $4)
			RETURNING id
		`, testWorkspaceID, fmt.Sprintf("WF %s %d", role, suffix), testRuntimeID, testUserID).Scan(&id); err != nil {
			t.Fatalf("create %s agent: %v", role, err)
		}
		return id
	}
	f.executorID = mkAgent("Executor")
	f.evaluatorID = mkAgent("Evaluator")

	// Bind node selectors to the fresh agent names.
	for i := range nodes {
		cfg, err := workflow.ParseNodeConfig(nodes[i].Config)
		if err != nil {
			t.Fatalf("parse node config: %v", err)
		}
		switch cfg.Role {
		case workflow.RoleExecutor, "":
			cfg.AgentSelector = fmt.Sprintf("WF Executor %d", suffix)
		case workflow.RoleEvaluator:
			cfg.AgentSelector = fmt.Sprintf("WF Evaluator %d", suffix)
		}
		raw, _ := json.Marshal(cfg)
		nodes[i].Config = raw
	}

	templates := workflow.NewTemplateService(testHandler.Queries, testPool)
	detail, err := templates.CreateTemplate(ctx, workflow.CreateTemplateParams{
		WorkspaceID: util.MustParseUUID(testWorkspaceID),
		Key:         fmt.Sprintf("%s-%d", key, suffix),
		Name:        key,
		CreatedBy:   util.MustParseUUID(testUserID),
		Nodes:       nodes,
		Edges:       edges,
	})
	if err != nil {
		t.Fatalf("create template: %v", err)
	}
	published, err := templates.PublishTemplate(ctx, util.MustParseUUID(testWorkspaceID), detail.Template.ID)
	if err != nil {
		t.Fatalf("publish template: %v", err)
	}
	f.templateID = published.Template.ID

	f.engine = workflow.NewEngine(testHandler.Queries, testPool, testHandler.IssueService, testHandler.TaskService, events.New())
	f.wh = NewWorkflowHandler(testHandler.Queries, f.engine)

	run, created, err := f.engine.StartRun(ctx, workflow.StartRunParams{
		WorkspaceID: util.MustParseUUID(testWorkspaceID),
		TemplateID:  published.Template.ID,
		SourceType:  "hook",
		SourceID:    fmt.Sprintf("ext-%d", suffix),
		Title:       "API fixture run",
		InitiatorID: util.MustParseUUID(testUserID),
	})
	if err != nil || !created {
		t.Fatalf("start run: %v (created=%v)", err, created)
	}
	f.run = run
	f.intakeIssue = run.IntakeIssueID
	t.Cleanup(func() { f.cleanup(t) })
	return f
}

// agentCfg is a shorthand for a node config JSON blob.
func agentCfg(t *testing.T, cfg workflow.NodeConfig) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	return raw
}

// twoNodeTemplate: work(executor, optional required exit field) → end.
func twoNodeTemplate(t *testing.T, withRequiredField bool) ([]workflow.NodeInput, []workflow.EdgeInput) {
	workCfg := workflow.NodeConfig{Role: workflow.RoleExecutor, Instructions: "Do the work"}
	if withRequiredField {
		workCfg.ExitFields = &workflow.ExitFieldsSchema{Fields: []workflow.ExitFieldSpec{
			{Name: "pr_url", Type: "string", Required: true},
		}}
	}
	return []workflow.NodeInput{
		{NodeKey: "work", Type: workflow.NodeTypeAgent, Name: "work", Config: agentCfg(t, workCfg)},
		{NodeKey: "end", Type: workflow.NodeTypeEnd, Name: "end", Config: agentCfg(t, workflow.NodeConfig{})},
	}, []workflow.EdgeInput{{FromNodeKey: "work", ToNodeKey: "end"}}
}

// threeNodeTemplate: work(executor) → gate(evaluator, optional required field) → end.
func threeNodeTemplate(t *testing.T, gateRequiredField bool) ([]workflow.NodeInput, []workflow.EdgeInput) {
	gateCfg := workflow.NodeConfig{Role: workflow.RoleEvaluator, Instructions: "Judge"}
	if gateRequiredField {
		gateCfg.ExitFields = &workflow.ExitFieldsSchema{Fields: []workflow.ExitFieldSpec{
			{Name: "verdict_score", Type: "number", Required: true},
		}}
	}
	return []workflow.NodeInput{
			{NodeKey: "work", Type: workflow.NodeTypeAgent, Name: "work", Config: agentCfg(t, workflow.NodeConfig{Role: workflow.RoleExecutor})},
			{NodeKey: "gate", Type: workflow.NodeTypeAgent, Name: "gate", Config: agentCfg(t, gateCfg)},
			{NodeKey: "end", Type: workflow.NodeTypeEnd, Name: "end", Config: agentCfg(t, workflow.NodeConfig{})},
		}, []workflow.EdgeInput{
			{FromNodeKey: "work", ToNodeKey: "gate"},
			{FromNodeKey: "gate", ToNodeKey: "end"},
		}
}

// stepTask returns the task id of a node's latest step.
func (f *workflowAPIFixture) stepTask(t *testing.T, nodeKey string) string {
	t.Helper()
	var taskID string
	if err := testPool.QueryRow(context.Background(), `
		SELECT agent_task_id::text FROM step_instance
		WHERE run_id = $1 AND node_key = $2 ORDER BY attempt DESC LIMIT 1
	`, f.run.ID, nodeKey).Scan(&taskID); err != nil {
		t.Fatalf("step task for %q: %v", nodeKey, err)
	}
	return taskID
}

// stepStatus returns the latest step status for a node.
func (f *workflowAPIFixture) stepStatus(t *testing.T, nodeKey string) string {
	t.Helper()
	var status string
	if err := testPool.QueryRow(context.Background(), `
		SELECT status FROM step_instance
		WHERE run_id = $1 AND node_key = $2 ORDER BY attempt DESC LIMIT 1
	`, f.run.ID, nodeKey).Scan(&status); err != nil {
		t.Fatalf("step status for %q: %v", nodeKey, err)
	}
	return status
}

// matRequest builds a request carrying the mat_-bound headers the auth
// middleware would stamp (task token path, middleware/auth.go:73-97).
func matRequest(t *testing.T, method, path, taskID, agentID string, body any) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Actor-Source", "task_token")
	req.Header.Set("X-Task-ID", taskID)
	req.Header.Set("X-Agent-ID", agentID)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	return withURLParam(req, "id", taskID)
}

func TestWorkflowSubmissionCreated(t *testing.T) {
	nodes, edges := twoNodeTemplate(t, true)
	f := setupWorkflowAPIFixture(t, "sub-created", nodes, edges)
	taskID := f.stepTask(t, "work")

	w := httptest.NewRecorder()
	req := matRequest(t, "POST", "/api/tasks/"+taskID+"/submission", taskID, f.executorID, map[string]any{
		"status":      "DONE",
		"exit_fields": map[string]any{"pr_url": "https://example/pr/1"},
	})
	f.wh.CreateSubmission(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	var resp submissionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Created || resp.ID == "" || resp.Status != "DONE" {
		t.Fatalf("resp = %+v", resp)
	}
	// The executor submission derived the system pass and advanced the run.
	if got := f.stepStatus(t, "work"); got != "passed" {
		t.Fatalf("work step = %q, want passed", got)
	}
	var result, by string
	if err := testPool.QueryRow(context.Background(), `
		SELECT v.result, v.verdict_by FROM verdict v
		JOIN step_instance si ON si.id = v.step_instance_id
		WHERE si.run_id = $1 AND si.node_key = 'work'
	`, f.run.ID).Scan(&result, &by); err != nil {
		t.Fatalf("read verdict: %v", err)
	}
	if result != "pass" || by != "system" {
		t.Fatalf("verdict = %q by %q, want pass by system", result, by)
	}
}

func TestWorkflowSubmissionMissingRequiredField(t *testing.T) {
	nodes, edges := twoNodeTemplate(t, true)
	f := setupWorkflowAPIFixture(t, "sub-missing", nodes, edges)
	taskID := f.stepTask(t, "work")

	w := httptest.NewRecorder()
	req := matRequest(t, "POST", "/api/tasks/"+taskID+"/submission", taskID, f.executorID, map[string]any{
		"status":      "DONE",
		"exit_fields": map[string]any{},
	})
	f.wh.CreateSubmission(w, req)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Error  string `json:"error"`
		Fields []struct {
			Name string `json:"name"`
			Code string `json:"code"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Fields) != 1 || body.Fields[0].Name != "pr_url" || body.Fields[0].Code != "missing" {
		t.Fatalf("fields = %+v, want one missing pr_url (AC3 structured error)", body.Fields)
	}
	// No submission, no verdict (AC3: 不进入 verdict).
	if got := f.stepStatus(t, "work"); got != "active" {
		t.Fatalf("work step = %q, want active (rejected before verdict)", got)
	}
}

func TestWorkflowSubmissionUnknownFieldsTolerated(t *testing.T) {
	nodes, edges := twoNodeTemplate(t, true)
	f := setupWorkflowAPIFixture(t, "sub-unknown", nodes, edges)
	taskID := f.stepTask(t, "work")

	w := httptest.NewRecorder()
	req := matRequest(t, "POST", "/api/tasks/"+taskID+"/submission", taskID, f.executorID, map[string]any{
		"status": "DONE",
		"exit_fields": map[string]any{
			"pr_url":       "https://example/pr/1",
			"future_field": map[string]any{"nested": true},
		},
	})
	f.wh.CreateSubmission(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (unknown fields tolerated, D-9); body=%s", w.Code, w.Body.String())
	}
	// Unknown fields pass through untouched.
	var stored string
	if err := testPool.QueryRow(context.Background(), `
		SELECT s.exit_fields::text FROM submission s
		JOIN step_instance si ON si.id = s.step_instance_id
		WHERE si.run_id = $1 AND si.node_key = 'work'
	`, f.run.ID).Scan(&stored); err != nil {
		t.Fatalf("read exit_fields: %v", err)
	}
	if !bytes.Contains([]byte(stored), []byte("future_field")) {
		t.Fatalf("exit_fields = %s, want unknown field preserved", stored)
	}
}

func TestWorkflowSubmissionBlockedSkipsRequiredFields(t *testing.T) {
	nodes, edges := twoNodeTemplate(t, true)
	f := setupWorkflowAPIFixture(t, "sub-blocked", nodes, edges)
	taskID := f.stepTask(t, "work")

	w := httptest.NewRecorder()
	req := matRequest(t, "POST", "/api/tasks/"+taskID+"/submission", taskID, f.executorID, map[string]any{
		"status":      "BLOCKED",
		"raw_summary": "need credentials",
	})
	f.wh.CreateSubmission(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (a blocked agent cannot produce exit fields); body=%s", w.Code, w.Body.String())
	}
	if got := f.stepStatus(t, "work"); got != "blocked" {
		t.Fatalf("work step = %q, want blocked", got)
	}
}

func TestWorkflowSubmissionRejectsWorkdirArtifacts(t *testing.T) {
	nodes, edges := twoNodeTemplate(t, false)
	f := setupWorkflowAPIFixture(t, "sub-artifacts", nodes, edges)
	taskID := f.stepTask(t, "work")

	// D-11: artifacts must carry durable references; a workdir-relative path
	// is garbage collected within 24h and rejected with a structured 400.
	w := httptest.NewRecorder()
	req := matRequest(t, "POST", "/api/tasks/"+taskID+"/submission", taskID, f.executorID, map[string]any{
		"status": "DONE",
		"artifacts": map[string]any{
			"report": "multica_workspaces/ws/task/workdir/report.md",
		},
	})
	f.wh.CreateSubmission(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Error  string `json:"error"`
		Fields []struct {
			Name string `json:"name"`
			Code string `json:"code"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Fields) != 1 || body.Fields[0].Code != "local_path" || body.Fields[0].Name != "artifacts.report" {
		t.Fatalf("fields = %+v, want one local_path at artifacts.report", body.Fields)
	}
	if got := f.stepStatus(t, "work"); got != "active" {
		t.Fatalf("work step = %q, want active (rejected before any write)", got)
	}
}

func TestWorkflowSubmissionIdempotentReplay(t *testing.T) {
	nodes, edges := twoNodeTemplate(t, false)
	f := setupWorkflowAPIFixture(t, "sub-idem", nodes, edges)
	taskID := f.stepTask(t, "work")

	body := map[string]any{
		"status":          "DONE",
		"idempotency_key": "cli-attempt-1",
	}
	w1 := httptest.NewRecorder()
	f.wh.CreateSubmission(w1, matRequest(t, "POST", "/api/tasks/"+taskID+"/submission", taskID, f.executorID, body))
	if w1.Code != http.StatusCreated {
		t.Fatalf("first = %d, want 201; body=%s", w1.Code, w1.Body.String())
	}
	var first submissionResponse
	json.Unmarshal(w1.Body.Bytes(), &first)

	w2 := httptest.NewRecorder()
	f.wh.CreateSubmission(w2, matRequest(t, "POST", "/api/tasks/"+taskID+"/submission", taskID, f.executorID, body))
	if w2.Code != http.StatusOK {
		t.Fatalf("replay = %d, want 200; body=%s", w2.Code, w2.Body.String())
	}
	var replay submissionResponse
	json.Unmarshal(w2.Body.Bytes(), &replay)
	if replay.ID != first.ID || replay.Created {
		t.Fatalf("replay = %+v, want same id + created=false", replay)
	}

	// A DIFFERENT key on the same step is a real conflict, not a replay.
	w3 := httptest.NewRecorder()
	f.wh.CreateSubmission(w3, matRequest(t, "POST", "/api/tasks/"+taskID+"/submission", taskID, f.executorID, map[string]any{
		"status":          "DONE",
		"idempotency_key": "cli-attempt-2",
	}))
	if w3.Code != http.StatusConflict {
		t.Fatalf("distinct key = %d, want 409; body=%s", w3.Code, w3.Body.String())
	}
}

func TestWorkflowSubmissionAuthRules(t *testing.T) {
	nodes, edges := twoNodeTemplate(t, false)
	f := setupWorkflowAPIFixture(t, "sub-auth", nodes, edges)
	taskID := f.stepTask(t, "work")

	// Non-task-token callers (PAT/JWT) get 403.
	w := httptest.NewRecorder()
	req := matRequest(t, "POST", "/api/tasks/"+taskID+"/submission", taskID, f.executorID, map[string]any{"status": "DONE"})
	req.Header.Del("X-Actor-Source")
	f.wh.CreateSubmission(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("non-mat caller = %d, want 403", w.Code)
	}

	// URL task != token-bound task gets 403.
	w = httptest.NewRecorder()
	req = matRequest(t, "POST", "/api/tasks/"+taskID+"/submission", taskID, f.executorID, map[string]any{"status": "DONE"})
	req.Header.Set("X-Task-ID", util.UUIDToString(pgtype.UUID{Valid: true})) // zero uuid ≠ URL
	f.wh.CreateSubmission(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("url/token mismatch = %d, want 403", w.Code)
	}
}

func TestWorkflowVerdictExecutorForbidden(t *testing.T) {
	nodes, edges := twoNodeTemplate(t, false)
	f := setupWorkflowAPIFixture(t, "verdict-403", nodes, edges)
	taskID := f.stepTask(t, "work")

	w := httptest.NewRecorder()
	req := matRequest(t, "POST", "/api/tasks/"+taskID+"/verdict", taskID, f.executorID, map[string]any{
		"result": "pass",
	})
	f.wh.CreateVerdict(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("executor verdict write = %d, want 403 (verdict actor model)", w.Code)
	}
}

func TestWorkflowVerdictEvaluatorFlow(t *testing.T) {
	nodes, edges := threeNodeTemplate(t, true)
	f := setupWorkflowAPIFixture(t, "verdict-flow", nodes, edges)

	// Drive the executor step through its submission first.
	workTask := f.stepTask(t, "work")
	w := httptest.NewRecorder()
	f.wh.CreateSubmission(w, matRequest(t, "POST", "/api/tasks/"+workTask+"/submission", workTask, f.executorID, map[string]any{
		"status": "DONE",
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("submission = %d; body=%s", w.Code, w.Body.String())
	}
	gateTask := f.stepTask(t, "gate")

	// Verdict without the required exit fields → 400 with the structured
	// field list (auto-created submission passes the same validation).
	w = httptest.NewRecorder()
	f.wh.CreateVerdict(w, matRequest(t, "POST", "/api/tasks/"+gateTask+"/verdict", gateTask, f.evaluatorID, map[string]any{
		"result": "fail",
	}))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("verdict missing fields = %d, want 400; body=%s", w.Code, w.Body.String())
	}
	var errBody struct {
		Fields []struct {
			Name string `json:"name"`
			Code string `json:"code"`
		} `json:"fields"`
	}
	json.Unmarshal(w.Body.Bytes(), &errBody)
	if len(errBody.Fields) != 1 || errBody.Fields[0].Name != "verdict_score" || errBody.Fields[0].Code != "missing" {
		t.Fatalf("fields = %+v, want missing verdict_score", errBody.Fields)
	}

	// Verdict WITH the required fields → 201; minimal submission auto-created
	// in the same tx; verdict_by=agent; chain advances.
	w = httptest.NewRecorder()
	f.wh.CreateVerdict(w, matRequest(t, "POST", "/api/tasks/"+gateTask+"/verdict", gateTask, f.evaluatorID, map[string]any{
		"result":      "pass",
		"confidence":  0.9,
		"exit_fields": map[string]any{"verdict_score": 0.95},
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("verdict = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	var resp verdictResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.VerdictBy != "agent" || resp.Result != "pass" {
		t.Fatalf("resp = %+v, want agent/pass", resp)
	}
	if resp.Confidence == nil || *resp.Confidence != 0.9 {
		t.Fatalf("confidence = %v, want 0.9", resp.Confidence)
	}
	var subStatus string
	if err := testPool.QueryRow(context.Background(), `
		SELECT s.status FROM submission s
		JOIN step_instance si ON si.id = s.step_instance_id
		WHERE si.run_id = $1 AND si.node_key = 'gate'
	`, f.run.ID).Scan(&subStatus); err != nil {
		t.Fatalf("auto-created submission missing: %v", err)
	}
	if subStatus != "DONE" {
		t.Fatalf("auto submission status = %q, want DONE", subStatus)
	}
	if got := f.stepStatus(t, "gate"); got != "passed" {
		t.Fatalf("gate step = %q, want passed", got)
	}

	// A second verdict on the same submission is a conflict.
	w = httptest.NewRecorder()
	f.wh.CreateVerdict(w, matRequest(t, "POST", "/api/tasks/"+gateTask+"/verdict", gateTask, f.evaluatorID, map[string]any{
		"result": "fail",
	}))
	if w.Code != http.StatusConflict {
		t.Fatalf("second verdict = %d, want 409; body=%s", w.Code, w.Body.String())
	}
}

func TestWorkflowGetVerdict(t *testing.T) {
	nodes, edges := twoNodeTemplate(t, false)
	f := setupWorkflowAPIFixture(t, "get-verdict", nodes, edges)
	taskID := f.stepTask(t, "work")

	// 404 before any verdict exists.
	w := httptest.NewRecorder()
	f.wh.GetVerdict(w, matRequest(t, "GET", "/api/tasks/"+taskID+"/verdict", taskID, f.executorID, nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("pre-verdict GET = %d, want 404", w.Code)
	}

	w = httptest.NewRecorder()
	f.wh.CreateSubmission(w, matRequest(t, "POST", "/api/tasks/"+taskID+"/submission", taskID, f.executorID, map[string]any{
		"status": "DONE",
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("submission = %d", w.Code)
	}

	w = httptest.NewRecorder()
	f.wh.GetVerdict(w, matRequest(t, "GET", "/api/tasks/"+taskID+"/verdict", taskID, f.executorID, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET verdict = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp verdictResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Result != "pass" || resp.VerdictBy != "system" {
		t.Fatalf("resp = %+v, want pass/system", resp)
	}
}

func TestWorkflowGetStepContext(t *testing.T) {
	nodes, edges := threeNodeTemplate(t, true)
	f := setupWorkflowAPIFixture(t, "step-ctx", nodes, edges)

	workTask := f.stepTask(t, "work")
	w := httptest.NewRecorder()
	f.wh.CreateSubmission(w, matRequest(t, "POST", "/api/tasks/"+workTask+"/submission", workTask, f.executorID, map[string]any{
		"status":      "DONE",
		"exit_fields": map[string]any{"branch": "feat/x"},
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("submission = %d; body=%s", w.Code, w.Body.String())
	}

	gateTask := f.stepTask(t, "gate")
	w = httptest.NewRecorder()
	f.wh.GetStepContext(w, matRequest(t, "GET", "/api/tasks/"+gateTask+"/step-context", gateTask, f.evaluatorID, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("step context = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp workflow.StepContext
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.NodeKey != "gate" || resp.Role != "evaluator" || resp.Instructions != "Judge" {
		t.Fatalf("resp = %+v", resp)
	}
	if resp.ExitFieldsSchema == nil || len(resp.ExitFieldsSchema.Fields) != 1 || resp.ExitFieldsSchema.Fields[0].Name != "verdict_score" {
		t.Fatalf("schema = %+v, want verdict_score field", resp.ExitFieldsSchema)
	}
	if resp.UpstreamNodeKey != "work" || resp.UpstreamExitFields["branch"] != "feat/x" {
		t.Fatalf("upstream = %q %+v, want work/branch (AC9 context contract)", resp.UpstreamNodeKey, resp.UpstreamExitFields)
	}
}

func TestWorkflowSubmissionRejectsLocalPathArtifacts(t *testing.T) {
	nodes, edges := twoNodeTemplate(t, false)
	f := setupWorkflowAPIFixture(t, "sub-artifacts", nodes, edges)
	taskID := f.stepTask(t, "work")

	w := httptest.NewRecorder()
	req := matRequest(t, "POST", "/api/tasks/"+taskID+"/submission", taskID, f.executorID, map[string]any{
		"status":    "DONE",
		"artifacts": map[string]any{"diff": "./workdir/out.diff"},
	})
	f.wh.CreateSubmission(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (D-11 local path rejected); body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Fields []struct {
			Code string `json:"code"`
		} `json:"fields"`
	}
	json.Unmarshal(w.Body.Bytes(), &body)
	if len(body.Fields) != 1 || body.Fields[0].Code != "local_path" {
		t.Fatalf("fields = %+v, want one local_path error", body.Fields)
	}
}

// TestWorkflowTerminalStepWritesConflict is the Gate-W1 follow-up: a write
// aimed at a TERMINAL step (passed/failed/skipped) is a 409 Conflict, not a
// 500 — the state machine would ignore the signal anyway, so the row would
// be pure noise. The idempotent-replay escape hatch (same idempotency key on
// a since-passed step returns the original row) is covered separately by
// TestWorkflowSubmissionIdempotentReplay.
func TestWorkflowTerminalStepWritesConflict(t *testing.T) {
	t.Run("submission on passed step", func(t *testing.T) {
		nodes, edges := twoNodeTemplate(t, false)
		f := setupWorkflowAPIFixture(t, "term-sub", nodes, edges)
		taskID := f.stepTask(t, "work")

		// Pass the step via its first submission (system verdict advances it).
		w := httptest.NewRecorder()
		f.wh.CreateSubmission(w, matRequest(t, "POST", "/api/tasks/"+taskID+"/submission", taskID, f.executorID, map[string]any{
			"status": "DONE",
		}))
		if w.Code != http.StatusCreated {
			t.Fatalf("first submission = %d; body=%s", w.Code, w.Body.String())
		}
		if got := f.stepStatus(t, "work"); got != "passed" {
			t.Fatalf("work step = %q, want passed", got)
		}

		// A fresh write (new idempotency key) on the terminal step → 409.
		w = httptest.NewRecorder()
		f.wh.CreateSubmission(w, matRequest(t, "POST", "/api/tasks/"+taskID+"/submission", taskID, f.executorID, map[string]any{
			"status":          "DONE",
			"idempotency_key": "different-key",
		}))
		if w.Code != http.StatusConflict {
			t.Fatalf("submission on passed step = %d, want 409; body=%s", w.Code, w.Body.String())
		}
	})

	t.Run("verdict on failed step", func(t *testing.T) {
		nodes, edges := threeNodeTemplate(t, false)
		f := setupWorkflowAPIFixture(t, "term-verdict", nodes, edges)

		// Pass work so the gate activates.
		workTask := f.stepTask(t, "work")
		w := httptest.NewRecorder()
		f.wh.CreateSubmission(w, matRequest(t, "POST", "/api/tasks/"+workTask+"/submission", workTask, f.executorID, map[string]any{
			"status": "DONE",
		}))
		if w.Code != http.StatusCreated {
			t.Fatalf("work submission = %d; body=%s", w.Code, w.Body.String())
		}

		// Fail the gate through its full retry budget (default max_attempts=3):
		// each fail verdict retries with a fresh attempt until the step lands
		// terminal 'failed'.
		for attempt := 1; attempt <= 3; attempt++ {
			gateTask := f.stepTask(t, "gate")
			w = httptest.NewRecorder()
			f.wh.CreateVerdict(w, matRequest(t, "POST", "/api/tasks/"+gateTask+"/verdict", gateTask, f.evaluatorID, map[string]any{
				"result":     "fail",
				"root_cause": "deficient",
			}))
			if w.Code != http.StatusCreated {
				t.Fatalf("fail verdict attempt %d = %d; body=%s", attempt, w.Code, w.Body.String())
			}
		}
		if got := f.stepStatus(t, "gate"); got != "failed" {
			t.Fatalf("gate step = %q, want failed", got)
		}

		// A verdict aimed at the terminal step → 409 (Gate-W1 follow-up).
		gateTask := f.stepTask(t, "gate")
		w = httptest.NewRecorder()
		f.wh.CreateVerdict(w, matRequest(t, "POST", "/api/tasks/"+gateTask+"/verdict", gateTask, f.evaluatorID, map[string]any{
			"result": "pass",
		}))
		if w.Code != http.StatusConflict {
			t.Fatalf("verdict on failed step = %d, want 409; body=%s", w.Code, w.Body.String())
		}
	})
}
