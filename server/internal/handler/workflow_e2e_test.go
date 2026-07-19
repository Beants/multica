package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/workflow"
)

// workflow_e2e_test.go — Wave 2 happy-path chain proof at the API level
// (implement.md 2.6): hook ingress → run created → executor submission →
// evaluator verdict → acceptance approve → run completed. Each hop uses the
// real HTTP handler surface (hook, mat_ submission/verdict, member
// acceptance), so the test doubles as the wiring proof that the pieces
// compose, not just that each works in isolation.

// e2eChainTemplate: work(executor) → gate(evaluator) → review(acceptance) → end.
func e2eChainTemplate(t *testing.T) ([]workflow.NodeInput, []workflow.EdgeInput) {
	return []workflow.NodeInput{
			{NodeKey: "work", Type: workflow.NodeTypeAgent, Name: "work", Config: agentCfg(t, workflow.NodeConfig{Role: workflow.RoleExecutor, Instructions: "Implement the requirement"})},
			{NodeKey: "gate", Type: workflow.NodeTypeAgent, Name: "gate", Config: agentCfg(t, workflow.NodeConfig{Role: workflow.RoleEvaluator, Instructions: "Judge the implementation"})},
			{NodeKey: "review", Type: workflow.NodeTypeAcceptance, Name: "review", Config: agentCfg(t, workflow.NodeConfig{})},
			{NodeKey: "end", Type: workflow.NodeTypeEnd, Name: "end", Config: agentCfg(t, workflow.NodeConfig{})},
		}, []workflow.EdgeInput{
			{FromNodeKey: "work", ToNodeKey: "gate"},
			{FromNodeKey: "gate", ToNodeKey: "review"},
			{FromNodeKey: "review", ToNodeKey: "end"},
		}
}

// stepTaskForRun returns the task id of a node's latest step in a run.
func stepTaskForRun(t *testing.T, runID, nodeKey string) string {
	t.Helper()
	var taskID string
	if err := testPool.QueryRow(context.Background(), `
		SELECT agent_task_id::text FROM step_instance
		WHERE run_id = $1 AND node_key = $2 ORDER BY attempt DESC LIMIT 1
	`, runID, nodeKey).Scan(&taskID); err != nil {
		t.Fatalf("step task for %q: %v", nodeKey, err)
	}
	return taskID
}

// stepStatusForRun returns the latest step status for a node in a run.
func stepStatusForRun(t *testing.T, runID, nodeKey string) string {
	t.Helper()
	var status string
	if err := testPool.QueryRow(context.Background(), `
		SELECT status FROM step_instance
		WHERE run_id = $1 AND node_key = $2 ORDER BY attempt DESC LIMIT 1
	`, runID, nodeKey).Scan(&status); err != nil {
		t.Fatalf("step status for %q: %v", nodeKey, err)
	}
	return status
}

func TestWorkflowHookToCompletionChain(t *testing.T) {
	nodes, edges := e2eChainTemplate(t)
	f := setupHookFixture(t, "e2e-chain", nodes, edges)
	engine := f.hh.Engine
	wh := NewWorkflowHandler(testHandler.Queries, engine)
	wrh := NewWorkflowRunHandler(testHandler.Queries, engine)

	// 1. Hook ingress creates the run and activates the work node.
	w := httptest.NewRecorder()
	f.hh.HandleInboundHook(w, hookRequest(t, f.token, map[string]any{
		"title":     "E2E requirement",
		"source_id": "e2e-src-chain",
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("hook = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	hookResp := decodeHookResponse(t, w)
	runID, _ := hookResp["run_id"].(string)
	if runID == "" || hookResp["created"] != true {
		t.Fatalf("hook resp = %v", hookResp)
	}
	if got := stepStatusForRun(t, runID, "work"); got != workflow.StepActive {
		t.Fatalf("work step = %q, want active after hook", got)
	}

	// 2. Executor submission (mat_ token bound to the work step's task):
	// system-derived pass advances the chain to the gate.
	workTask := stepTaskForRun(t, runID, "work")
	w = httptest.NewRecorder()
	wh.CreateSubmission(w, matRequest(t, "POST", "/api/tasks/"+workTask+"/submission", workTask, f.executorID, map[string]any{
		"status":      "DONE",
		"exit_fields": map[string]any{"pr_url": "https://example/pr/1"},
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("submission = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	if got := stepStatusForRun(t, runID, "work"); got != workflow.StepPassed {
		t.Fatalf("work step = %q, want passed", got)
	}
	if got := stepStatusForRun(t, runID, "gate"); got != workflow.StepActive {
		t.Fatalf("gate step = %q, want active after work passed", got)
	}

	// 3. Evaluator verdict on the gate (verdict actor model): pass advances
	// the chain into the acceptance node, parking the run.
	gateTask := stepTaskForRun(t, runID, "gate")
	w = httptest.NewRecorder()
	wh.CreateVerdict(w, matRequest(t, "POST", "/api/tasks/"+gateTask+"/verdict", gateTask, f.evaluatorID, map[string]any{
		"result":     "pass",
		"confidence": 0.9,
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("verdict = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	if got := stepStatusForRun(t, runID, "gate"); got != workflow.StepPassed {
		t.Fatalf("gate step = %q, want passed", got)
	}
	var runStatus string
	if err := testPool.QueryRow(context.Background(),
		`SELECT status FROM workflow_run WHERE id = $1`, runID).Scan(&runStatus); err != nil {
		t.Fatalf("read run status: %v", err)
	}
	if runStatus != workflow.RunWaitingAcceptance {
		t.Fatalf("run = %q, want waiting_acceptance", runStatus)
	}

	// 4. Acceptance approve (member caller): the review step passes, the end
	// node closes the chain, and the run completes.
	w = httptest.NewRecorder()
	wrh.ApproveAcceptance(w, runRequest(t, "POST", "/api/workflow-runs/"+runID+"/acceptance/approve", runID, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("approve = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if err := testPool.QueryRow(context.Background(),
		`SELECT status FROM workflow_run WHERE id = $1`, runID).Scan(&runStatus); err != nil {
		t.Fatalf("read run status: %v", err)
	}
	if runStatus != workflow.RunCompleted {
		t.Fatalf("run = %q, want completed", runStatus)
	}
	for _, node := range []string{"work", "gate", "review", "end"} {
		if got := stepStatusForRun(t, runID, node); got != workflow.StepPassed {
			t.Fatalf("%s step = %q, want passed", node, got)
		}
	}

	// 5. The intake issue closed with the run, and the verdict actors are
	// recorded per the actor model (system for the executor, agent for the
	// evaluator).
	var intakeStatus string
	if err := testPool.QueryRow(context.Background(), `
		SELECT i.status FROM issue i
		JOIN workflow_run r ON r.intake_issue_id = i.id
		WHERE r.id = $1
	`, runID).Scan(&intakeStatus); err != nil {
		t.Fatalf("read intake status: %v", err)
	}
	if intakeStatus != "done" {
		t.Fatalf("intake issue = %q, want done", intakeStatus)
	}
	rows, err := testPool.Query(context.Background(), `
		SELECT si.node_key, v.result, v.verdict_by FROM verdict v
		JOIN step_instance si ON si.id = v.step_instance_id
		WHERE si.run_id = $1 ORDER BY v.created_at
	`, runID)
	if err != nil {
		t.Fatalf("list verdicts: %v", err)
	}
	defer rows.Close()
	verdictBy := map[string]string{}
	for rows.Next() {
		var node, result, by string
		if err := rows.Scan(&node, &result, &by); err != nil {
			t.Fatalf("scan verdict: %v", err)
		}
		if result != "pass" {
			t.Fatalf("verdict on %s = %q, want pass", node, result)
		}
		verdictBy[node] = by
	}
	if verdictBy["work"] != "system" || verdictBy["gate"] != "agent" {
		t.Fatalf("verdict actors = %v, want work=system gate=agent", verdictBy)
	}

	// 6. The full chain is traceable (AC4): every hop wrote its transitions.
	var transitionCount int
	if err := testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM step_transition WHERE run_id = $1`, runID).Scan(&transitionCount); err != nil {
		t.Fatalf("count transitions: %v", err)
	}
	// work: none→active, active→passed; gate: none→pending, pending→active,
	// active→passed; review: none→pending, pending→active, active→passed;
	// end: none→pending, pending→active, active→passed.
	if transitionCount < 11 {
		t.Fatalf("transitions = %d, want >= 11 (full chain traceable)", transitionCount)
	}
}

// TestWorkflowHookToCompletionChainIdempotentReplay checks the chain's
// ingress idempotency one more time end-to-end: re-pushing the same source_id
// after the run completed returns the SAME run without duplicating steps.
func TestWorkflowHookToCompletionChainIdempotentReplay(t *testing.T) {
	nodes, edges := e2eChainTemplate(t)
	f := setupHookFixture(t, "e2e-replay", nodes, edges)

	sourceID := "e2e-src-replay"
	w := httptest.NewRecorder()
	f.hh.HandleInboundHook(w, hookRequest(t, f.token, map[string]any{"title": "replay", "source_id": sourceID}))
	if w.Code != http.StatusCreated {
		t.Fatalf("first hook = %d; body=%s", w.Code, w.Body.String())
	}
	first := decodeHookResponse(t, w)

	w = httptest.NewRecorder()
	f.hh.HandleInboundHook(w, hookRequest(t, f.token, map[string]any{"title": "replay", "source_id": sourceID}))
	if w.Code != http.StatusOK {
		t.Fatalf("replay hook = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	second := decodeHookResponse(t, w)
	if second["run_id"] != first["run_id"] || second["created"] != false {
		t.Fatalf("replay = %v, want same run + created=false", second)
	}
	if n := countRunsForSource(t, sourceID); n != 1 {
		t.Fatalf("runs for source = %d, want 1", n)
	}
}
