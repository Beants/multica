package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/workflow"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// workflow_run_test.go — API contract tests for the run list/detail and
// acceptance decision endpoints. Engine-side acceptance semantics (guarded
// decide, rework, circuit breakers) are covered by internal/workflow tests;
// here we assert the transport: workspace scoping, member-as-reviewer, and
// sentinel → HTTP mapping.

// acceptanceTemplate: work(executor) → review(acceptance) → end.
func acceptanceTemplate(t *testing.T) ([]workflow.NodeInput, []workflow.EdgeInput) {
	return []workflow.NodeInput{
			{NodeKey: "work", Type: workflow.NodeTypeAgent, Name: "work", Config: agentCfg(t, workflow.NodeConfig{Role: workflow.RoleExecutor})},
			{NodeKey: "review", Type: workflow.NodeTypeAcceptance, Name: "review", Config: agentCfg(t, workflow.NodeConfig{})},
			{NodeKey: "end", Type: workflow.NodeTypeEnd, Name: "end", Config: agentCfg(t, workflow.NodeConfig{})},
		}, []workflow.EdgeInput{
			{FromNodeKey: "work", ToNodeKey: "review"},
			{FromNodeKey: "review", ToNodeKey: "end"},
		}
}

// runHandlerFor builds the run API handler over the fixture engine.
func runHandlerFor(f *workflowAPIFixture) *WorkflowRunHandler {
	return NewWorkflowRunHandler(testHandler.Queries, f.engine)
}

// driveToWaiting passes the work step so the run parks on the acceptance.
func driveToWaiting(t *testing.T, f *workflowAPIFixture) {
	t.Helper()
	taskID, err := util.ParseUUID(f.stepTask(t, "work"))
	if err != nil {
		t.Fatalf("parse task id: %v", err)
	}
	if _, _, err := f.engine.RecordSubmission(context.Background(), taskID, workflow.SubmissionInput{
		Status: workflow.SubmissionDone,
	}); err != nil {
		t.Fatalf("record work submission: %v", err)
	}
	var status string
	if err := testPool.QueryRow(context.Background(),
		`SELECT status FROM workflow_run WHERE id = $1`, f.run.ID).Scan(&status); err != nil {
		t.Fatalf("reload run: %v", err)
	}
	if status != workflow.RunWaitingAcceptance {
		t.Fatalf("run status = %q, want waiting_acceptance", status)
	}
}

func runRequest(t *testing.T, method, path, runID string, body any) *http.Request {
	t.Helper()
	return withURLParam(memberRequest(t, method, path, body), "id", runID)
}

func currentMemberID(t *testing.T) string {
	t.Helper()
	var id string
	if err := testPool.QueryRow(context.Background(),
		`SELECT id FROM member WHERE workspace_id = $1 AND user_id = $2`,
		testWorkspaceID, testUserID).Scan(&id); err != nil {
		t.Fatalf("load member: %v", err)
	}
	return id
}

func acceptanceByRun(t *testing.T, runID pgtype.UUID) db.Acceptance {
	t.Helper()
	accs, err := testHandler.Queries.ListAcceptancesForRun(context.Background(), runID)
	if err != nil || len(accs) == 0 {
		t.Fatalf("list acceptances: %v (n=%d)", err, len(accs))
	}
	return accs[0]
}

// TestWorkflowRunListAndDetail: the run appears in the workspace list, and
// the detail carries steps + submissions + verdicts + acceptances +
// transitions + the frozen snapshot (AC4 trace view).
func TestWorkflowRunListAndDetail(t *testing.T) {
	nodes, edges := acceptanceTemplate(t)
	f := setupWorkflowAPIFixture(t, "run-detail", nodes, edges)
	h := runHandlerFor(f)
	driveToWaiting(t, f)

	// List.
	wl := httptest.NewRecorder()
	h.ListRuns(wl, memberRequest(t, "GET", "/api/workflow-runs", nil))
	if wl.Code != http.StatusOK {
		t.Fatalf("list = %d, want 200; body=%s", wl.Code, wl.Body.String())
	}
	var list []map[string]any
	if err := json.Unmarshal(wl.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	found := false
	for _, item := range list {
		if item["id"] == uuidToString(f.run.ID) {
			found = true
			if item["status"] != workflow.RunWaitingAcceptance {
				t.Fatalf("list status = %v, want waiting_acceptance", item["status"])
			}
			if item["source_type"] != "hook" {
				t.Fatalf("list source_type = %v, want hook", item["source_type"])
			}
		}
	}
	if !found {
		t.Fatalf("run %s missing from list", uuidToString(f.run.ID))
	}

	// Detail.
	w := httptest.NewRecorder()
	h.GetRun(w, runRequest(t, "GET", "/api/workflow-runs/"+uuidToString(f.run.ID), uuidToString(f.run.ID), nil))
	if w.Code != http.StatusOK {
		t.Fatalf("detail = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var detail map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	steps, _ := detail["steps"].([]any)
	if len(steps) != 3 {
		t.Fatalf("steps = %d, want 3 (work/review/end): %v", len(steps), steps)
	}
	byNode := map[string]map[string]any{}
	for _, s := range steps {
		sm := s.(map[string]any)
		byNode[sm["node_key"].(string)] = sm
	}
	if byNode["work"]["status"] != workflow.StepPassed {
		t.Fatalf("work step = %v, want passed", byNode["work"]["status"])
	}
	if byNode["review"]["status"] != workflow.StepActive {
		t.Fatalf("review step = %v, want active", byNode["review"]["status"])
	}
	if subs, _ := detail["submissions"].([]any); len(subs) != 1 {
		t.Fatalf("submissions = %d, want 1", len(subs))
	}
	if verdicts, _ := detail["verdicts"].([]any); len(verdicts) != 1 {
		t.Fatalf("verdicts = %d, want 1", len(verdicts))
	}
	accs, _ := detail["acceptances"].([]any)
	if len(accs) != 1 || accs[0].(map[string]any)["status"] != "pending" {
		t.Fatalf("acceptances = %v, want one pending", accs)
	}
	if transitions, _ := detail["transitions"].([]any); len(transitions) == 0 {
		t.Fatalf("transitions empty")
	}
	if snap, _ := detail["template_snapshot"].(map[string]any); snap["key"] == nil {
		t.Fatalf("template_snapshot missing: %v", detail["template_snapshot"])
	}
}

// TestWorkflowRunAcceptanceApprove: approve parks → the acceptance is decided
// by the CURRENT MEMBER and the chain rolls through the end node to
// completed.
func TestWorkflowRunAcceptanceApprove(t *testing.T) {
	nodes, edges := acceptanceTemplate(t)
	f := setupWorkflowAPIFixture(t, "run-approve", nodes, edges)
	h := runHandlerFor(f)
	driveToWaiting(t, f)

	runID := uuidToString(f.run.ID)
	w := httptest.NewRecorder()
	h.ApproveAcceptance(w, runRequest(t, "POST", "/api/workflow-runs/"+runID+"/acceptance/approve", runID, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("approve = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["status"] != "approved" {
		t.Fatalf("resp status = %v, want approved", resp)
	}

	acc := acceptanceByRun(t, f.run.ID)
	if acc.Status != "approved" {
		t.Fatalf("acceptance = %q, want approved", acc.Status)
	}
	if got := uuidToString(acc.ReviewerID); got != currentMemberID(t) {
		t.Fatalf("reviewer = %q, want current member %q", got, currentMemberID(t))
	}
	if !acc.DecidedAt.Valid {
		t.Fatalf("decided_at not set")
	}

	var runStatus string
	if err := testPool.QueryRow(context.Background(),
		`SELECT status FROM workflow_run WHERE id = $1`, f.run.ID).Scan(&runStatus); err != nil {
		t.Fatalf("reload run: %v", err)
	}
	if runStatus != workflow.RunCompleted {
		t.Fatalf("run = %q, want completed (end node rolled through)", runStatus)
	}
}

// TestWorkflowRunAcceptanceReject: reject with target + reason starts
// targeted rework — run back to running, target node re-activated on a fresh
// attempt — and records the decision fields on the acceptance row.
func TestWorkflowRunAcceptanceReject(t *testing.T) {
	nodes, edges := acceptanceTemplate(t)
	f := setupWorkflowAPIFixture(t, "run-reject", nodes, edges)
	h := runHandlerFor(f)
	driveToWaiting(t, f)

	runID := uuidToString(f.run.ID)
	w := httptest.NewRecorder()
	h.RejectAcceptance(w, runRequest(t, "POST", "/api/workflow-runs/"+runID+"/acceptance/reject", runID, map[string]any{
		"reject_to_node_key": "work",
		"reason":             "implementation missed the requirement",
	}))
	if w.Code != http.StatusOK {
		t.Fatalf("reject = %d, want 200; body=%s", w.Code, w.Body.String())
	}

	acc := acceptanceByRun(t, f.run.ID)
	if acc.Status != "rejected" {
		t.Fatalf("acceptance = %q, want rejected", acc.Status)
	}
	if !acc.RejectReason.Valid || acc.RejectReason.String == "" {
		t.Fatalf("reject reason not recorded")
	}
	if !acc.RejectToNodeKey.Valid || acc.RejectToNodeKey.String != "work" {
		t.Fatalf("reject_to_node_key = %v, want work", acc.RejectToNodeKey)
	}

	var runStatus string
	if err := testPool.QueryRow(context.Background(),
		`SELECT status FROM workflow_run WHERE id = $1`, f.run.ID).Scan(&runStatus); err != nil {
		t.Fatalf("reload run: %v", err)
	}
	if runStatus != workflow.RunRunning {
		t.Fatalf("run = %q, want running after reject", runStatus)
	}
	// Target node re-activated on attempt 2.
	if got := f.stepStatus(t, "work"); got != workflow.StepActive && got != workflow.StepDispatched && got != workflow.StepRunning {
		t.Fatalf("work step = %q, want re-activated", got)
	}
	var attempt int
	if err := testPool.QueryRow(context.Background(), `
		SELECT attempt FROM step_instance WHERE run_id = $1 AND node_key = 'work'
		ORDER BY attempt DESC LIMIT 1`, f.run.ID).Scan(&attempt); err != nil {
		t.Fatalf("read attempt: %v", err)
	}
	if attempt != 2 {
		t.Fatalf("work attempt = %d, want 2", attempt)
	}
}

// TestWorkflowRunAcceptanceDecisionValidation: 400 for missing reject fields,
// 409 when the run has no pending acceptance, 404 for unknown / cross-
// workspace runs.
func TestWorkflowRunAcceptanceDecisionValidation(t *testing.T) {
	nodes, edges := acceptanceTemplate(t)
	f := setupWorkflowAPIFixture(t, "run-validation", nodes, edges)
	h := runHandlerFor(f)
	runID := uuidToString(f.run.ID)

	t.Run("reject requires target", func(t *testing.T) {
		w := httptest.NewRecorder()
		h.RejectAcceptance(w, runRequest(t, "POST", "/api/workflow-runs/"+runID+"/acceptance/reject", runID, map[string]any{
			"reason": "x",
		}))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("missing target = %d, want 400; body=%s", w.Code, w.Body.String())
		}
	})

	t.Run("reject requires reason", func(t *testing.T) {
		w := httptest.NewRecorder()
		h.RejectAcceptance(w, runRequest(t, "POST", "/api/workflow-runs/"+runID+"/acceptance/reject", runID, map[string]any{
			"reject_to_node_key": "work",
		}))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("missing reason = %d, want 400; body=%s", w.Code, w.Body.String())
		}
	})

	t.Run("no pending acceptance is 409", func(t *testing.T) {
		// The fixture run just started: the work step is in flight, no
		// acceptance has been activated.
		w := httptest.NewRecorder()
		h.ApproveAcceptance(w, runRequest(t, "POST", "/api/workflow-runs/"+runID+"/acceptance/approve", runID, nil))
		if w.Code != http.StatusConflict {
			t.Fatalf("approve without pending = %d, want 409; body=%s", w.Code, w.Body.String())
		}
	})

	t.Run("unknown run is 404", func(t *testing.T) {
		missing := "00000000-0000-0000-0000-000000000042"
		w := httptest.NewRecorder()
		h.ApproveAcceptance(w, runRequest(t, "POST", "/api/workflow-runs/"+missing+"/acceptance/approve", missing, nil))
		if w.Code != http.StatusNotFound {
			t.Fatalf("unknown run = %d, want 404", w.Code)
		}
	})

	t.Run("cross-workspace run is 404", func(t *testing.T) {
		// Inject a DIFFERENT workspace context: the run must not resolve.
		member, err := testHandler.Queries.GetMemberByUserAndWorkspace(context.Background(), db.GetMemberByUserAndWorkspaceParams{
			UserID:      util.MustParseUUID(testUserID),
			WorkspaceID: util.MustParseUUID(testWorkspaceID),
		})
		if err != nil {
			t.Fatalf("load member: %v", err)
		}
		otherWorkspace := "00000000-0000-0000-0000-0000000000aa"
		req := newRequest("GET", "/api/workflow-runs/"+runID, nil)
		req = withURLParam(req, "id", runID)
		req = req.WithContext(middleware.SetMemberContext(req.Context(), otherWorkspace, member))
		w := httptest.NewRecorder()
		h.GetRun(w, req)
		if w.Code != http.StatusNotFound {
			t.Fatalf("cross-workspace detail = %d, want 404", w.Code)
		}
	})
}

// TestWorkflowRunAcceptanceDoubleDecideConflict: the second decision on an
// already-decided acceptance is a 409 (guarded write, blueprint §8.3).
func TestWorkflowRunAcceptanceDoubleDecideConflict(t *testing.T) {
	nodes, edges := acceptanceTemplate(t)
	f := setupWorkflowAPIFixture(t, fmt.Sprintf("run-double-%d", time.Now().UnixNano()), nodes, edges)
	h := runHandlerFor(f)
	driveToWaiting(t, f)

	runID := uuidToString(f.run.ID)
	w1 := httptest.NewRecorder()
	h.ApproveAcceptance(w1, runRequest(t, "POST", "/api/workflow-runs/"+runID+"/acceptance/approve", runID, nil))
	if w1.Code != http.StatusOK {
		t.Fatalf("first approve = %d, want 200; body=%s", w1.Code, w1.Body.String())
	}
	// The decided acceptance is gone from pending → the handler's own
	// no-pending-acceptance 409 fires before the engine guard.
	w2 := httptest.NewRecorder()
	h.ApproveAcceptance(w2, runRequest(t, "POST", "/api/workflow-runs/"+runID+"/acceptance/approve", runID, nil))
	if w2.Code != http.StatusConflict {
		t.Fatalf("second approve = %d, want 409; body=%s", w2.Code, w2.Body.String())
	}
}
