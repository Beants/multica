package handler

// workflow_run_diagnosis_test.go — P1-6 AC3/AC4/AC5 unit tests for the
// pure aggregator buildRunDiagnosis. Hand-built db rows exercise every
// failure shape (verdict-fail, gate-reject, rework chain, sweeper reset,
// clean pass) without standing up the DB fixture.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func diagUUID(s string) pgtype.UUID {
	u, err := util.ParseUUID(s)
	if err != nil {
		panic(err)
	}
	return u
}

func diagByText(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: s != ""}
}

// TestBuildRunDiagnosis_SevenElements drives a run with one step per failure
// shape and pins the full seven-element contract (AC3) plus the ok/fail
// classification (AC4) for each.
func TestBuildRunDiagnosis_SevenElements(t *testing.T) {
	runID := diagUUID("11111111-1111-1111-1111-111111111111")
	taskID := diagUUID("22222222-2222-2222-2222-222222222222")
	agentID := diagUUID("33333333-3333-3333-3333-333333333333")
	stepFail := diagUUID("aaaa0000-0000-0000-0000-000000000001")
	stepGate := diagUUID("aaaa0000-0000-0000-0000-000000000002")
	stepPass := diagUUID("aaaa0000-0000-0000-0000-000000000003")
	stepRework := diagUUID("aaaa0000-0000-0000-0000-000000000004")
	stepSweep := diagUUID("aaaa0000-0000-0000-0000-000000000005")

	run := db.WorkflowRun{ID: runID, Status: "running"}

	steps := []db.StepInstance{
		{ID: stepFail, RunID: runID, NodeKey: "work", Status: "failed", AgentTaskID: taskID, AgentID: agentID, Attempt: 1},
		{ID: stepGate, RunID: runID, NodeKey: "gate", Status: "blocked", Attempt: 1},
		{ID: stepPass, RunID: runID, NodeKey: "review", Status: "passed", Attempt: 1},
		{ID: stepRework, RunID: runID, NodeKey: "plan", Status: "rework", Attempt: 2},
		{ID: stepSweep, RunID: runID, NodeKey: "build", Status: "active", Attempt: 1},
	}

	transitions := []db.StepTransition{
		{ID: diagUUID("b0000001-0000-0000-0000-000000000001"), StepInstanceID: stepFail, FromStatus: "running", ToStatus: "failed", Attempt: 1, TriggerBy: "verdict"},
		{ID: diagUUID("b0000002-0000-0000-0000-000000000002"), StepInstanceID: stepSweep, FromStatus: "running", ToStatus: "active", Attempt: 1, TriggerBy: "sweeper"},
	}

	tasks := []db.AgentTaskQueue{
		{ID: taskID, AgentID: agentID, Status: "failed", FailureReason: diagByText("OOM killed by kernel"), MaxAttempts: 3, Attempt: 1},
	}

	gates := []db.GateRun{
		{ID: diagUUID("c0000001-0000-0000-0000-000000000001"), StepInstanceID: stepGate, GateType: "script", Status: "block", Output: []byte(`{"stdout":"ok","stderr":"missing required field X"}`)},
	}

	got := buildRunDiagnosis(run, steps, transitions, tasks, gates)

	if got.RunID != util.UUIDToString(runID) {
		t.Fatalf("run_id = %q, want %q", got.RunID, util.UUIDToString(runID))
	}
	if got.Status != "running" {
		t.Fatalf("run_status = %q, want running", got.Status)
	}
	if len(got.Steps) != 5 {
		t.Fatalf("steps = %d, want 5", len(got.Steps))
	}

	byNode := make(map[string]stepDiagnosisDTO, len(got.Steps))
	for _, s := range got.Steps {
		byNode[s.NodeKey] = s
	}

	// AC3 ① — failed step carries run/step/task ids.
	work := byNode["work"]
	if work.TaskID == nil || *work.TaskID != util.UUIDToString(taskID) {
		t.Fatalf("work.task_id = %v, want %q", work.TaskID, util.UUIDToString(taskID))
	}
	// AC3 ② — agent id present.
	if work.AgentID == nil || *work.AgentID != util.UUIDToString(agentID) {
		t.Fatalf("work.agent_id = %v, want %q", work.AgentID, util.UUIDToString(agentID))
	}
	// AC3 ③④ — failure_type + reason derived from task.FailureReason.
	if work.FailureType != "fail" {
		t.Fatalf("work.failure_type = %q, want fail", work.FailureType)
	}
	if !strings.Contains(work.Reason, "OOM killed") {
		t.Fatalf("work.reason = %q, want contains 'OOM killed'", work.Reason)
	}
	// AC3 ⑥ — attempt + max_attempts.
	if work.Attempt != 1 || work.MaxAttempts == nil || *work.MaxAttempts != 3 {
		t.Fatalf("work attempt/max = %d/%v, want 1/3", work.Attempt, work.MaxAttempts)
	}
	// AC3 ⑦ + AC4 — final status + ok flag.
	if work.FinalStatus != "failed" || work.OK {
		t.Fatalf("work status/ok = %q/%v, want failed/false", work.FinalStatus, work.OK)
	}
	// AC3 ⑥ — transition timeline attached.
	if len(work.Transitions) != 1 || work.Transitions[0].TriggerBy != "verdict" {
		t.Fatalf("work.transitions = %+v, want 1 verdict entry", work.Transitions)
	}

	// AC3 ⑤ + gate-reject classification — stderr surfaced in reason, output attached.
	gate := byNode["gate"]
	if gate.FailureType != "gate_reject" {
		t.Fatalf("gate.failure_type = %q, want gate_reject", gate.FailureType)
	}
	if !strings.Contains(gate.Reason, "missing required field X") {
		t.Fatalf("gate.reason = %q, want contains stderr text", gate.Reason)
	}
	if len(gate.Output) == 0 {
		t.Fatalf("gate.output empty; want gate_run output JSONB attached")
	}

	// AC4 — clean pass: ok=true, no failure_type, empty reason.
	pass := byNode["review"]
	if !pass.OK || pass.FailureType != "" || pass.Reason != "" {
		t.Fatalf("passed step = ok=%v type=%q reason=%q; want ok=true/empty/empty", pass.OK, pass.FailureType, pass.Reason)
	}

	// rework chain: ok=false, failure_type=rework, attempt carried.
	rw := byNode["plan"]
	if rw.OK || rw.FailureType != "rework" || rw.Attempt != 2 {
		t.Fatalf("rework step = ok=%v type=%q attempt=%d; want false/rework/2", rw.OK, rw.FailureType, rw.Attempt)
	}

	// sweeper reset is informational: active step stays ok=true but reason
	// records the self-heal event.
	sw := byNode["build"]
	if !sw.OK {
		t.Fatalf("sweeper-reset active step ok=%v, want true (reset is self-heal, not failure)", sw.OK)
	}
	if !strings.Contains(sw.Reason, "sweeper") {
		t.Fatalf("sweep reason = %q, want contains 'sweeper'", sw.Reason)
	}
}

// TestExtractStderrRobustness pins the parse-failure contract: a malformed
// or empty gate_run output never breaks diagnosis.
func TestExtractStderrRobustness(t *testing.T) {
	cases := map[string]string{
		`{"stderr":"boom"}`:    "boom",
		`{"stdout":"x"}`:       "",
		`{`:                     "",
		``:                      "",
		`{"stderr":"  trim  "}`: "trim",
	}
	for in, want := range cases {
		if got := extractStderr([]byte(in)); got != want {
			t.Errorf("extractStderr(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestBuildRunDiagnosis_EmptyRun pins the zero-step degenerate case (a run
// with no steps yet must return an empty slice, not nil, for stable JSON).
func TestBuildRunDiagnosis_EmptyRun(t *testing.T) {
	run := db.WorkflowRun{ID: diagUUID("11111111-1111-1111-1111-111111111111"), Status: "running"}
	got := buildRunDiagnosis(run, nil, nil, nil, nil)
	if got.Steps == nil {
		t.Fatal("Steps nil; want non-nil empty slice")
	}
	if len(got.Steps) != 0 {
		t.Fatalf("Steps = %d, want 0", len(got.Steps))
	}
	// JSON marshal must succeed (stable empty-array serialization).
	if _, err := json.Marshal(got); err != nil {
		t.Fatalf("marshal empty diagnosis: %v", err)
	}
}

// TestGetRunDiagnosis wires the handler end-to-end (AC2): a driven run's
// diagnosis comes back 200 with the work step carrying its dispatched
// task_id (七要素 ①). The auth path (runInWorkspace) is shared with GetRun
// and exercised by TestWorkflowRunListAndDetail's workspace scoping (AC6).
func TestGetRunDiagnosis(t *testing.T) {
	nodes, edges := acceptanceTemplate(t)
	f := setupWorkflowAPIFixture(t, "run-diagnosis", nodes, edges)
	h := runHandlerFor(f)
	driveToWaiting(t, f)

	w := httptest.NewRecorder()
	h.GetRunDiagnosis(w, runRequest(t, "GET", "/api/workflow-runs/"+uuidToString(f.run.ID)+"/diagnosis", uuidToString(f.run.ID), nil))
	if w.Code != http.StatusOK {
		t.Fatalf("diagnosis = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var got runDiagnosisDTO
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode diagnosis: %v", err)
	}
	if got.RunID != uuidToString(f.run.ID) {
		t.Fatalf("run_id = %q, want %q", got.RunID, uuidToString(f.run.ID))
	}
	var work *stepDiagnosisDTO
	for i := range got.Steps {
		if got.Steps[i].NodeKey == "work" {
			work = &got.Steps[i]
		}
	}
	if work == nil {
		t.Fatalf("work step missing from diagnosis; nodes=%v", diagNodeKeys(got.Steps))
	}
	if work.TaskID == nil {
		t.Fatalf("work.task_id nil; want dispatched agent_task_id (七要素 ①)")
	}
	if work.FinalStatus != "passed" {
		t.Fatalf("work.final_status = %q, want passed (submission recorded)", work.FinalStatus)
	}
	if !work.OK {
		t.Fatalf("work.ok = false; want true (passed step)")
	}
}

func diagNodeKeys(steps []stepDiagnosisDTO) []string {
	out := make([]string, len(steps))
	for i, s := range steps {
		out[i] = s.NodeKey
	}
	return out
}
