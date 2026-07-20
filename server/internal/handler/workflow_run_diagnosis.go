package handler

// workflow_run_diagnosis.go — P1-6 failure diagnosis API. Aggregates the
// seven elements of 军团文 §4.5 (① run/step/task id ② agent/executor
// ③ failure type ④ failure reason ⑤ stderr/stdout summary ⑥ retry history
// ⑦ final status) for every step of a run from four EXISTING tables
// (step_instance + step_transition + agent_task_queue + gate_run). Read-only:
// no business-table writes, no new tables (step_transition is the P0 history
// source design.md §2 支柱 4 calls out for exactly this). The aggregator is
// a pure function (buildRunDiagnosis) so it has direct unit tests independent
// of the DB fixture.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// stepDiagnosisDTO carries the seven elements for one step.
type stepDiagnosisDTO struct {
	StepID      string                  `json:"step_id"`
	NodeKey     string                  `json:"node_key"`
	RunID       string                  `json:"run_id"`
	TaskID      *string                 `json:"task_id,omitempty"`      // ①
	AgentID     *string                 `json:"agent_id,omitempty"`     // ②
	Attempt     int32                   `json:"attempt"`                // ⑥
	MaxAttempts *int32                  `json:"max_attempts,omitempty"` // ⑥
	FinalStatus string                  `json:"final_status"`           // ⑦
	OK          bool                    `json:"ok"`
	FailureType string                  `json:"failure_type,omitempty"` // ③
	Reason      string                  `json:"reason,omitempty"`       // ④
	Output      json.RawMessage         `json:"output,omitempty"`       // ⑤
	Transitions []workflowTransitionDTO `json:"transitions"`            // ⑥
}

type runDiagnosisDTO struct {
	RunID  string             `json:"run_id"`
	Status string             `json:"run_status"`
	Steps  []stepDiagnosisDTO `json:"steps"`
}

// GetRunDiagnosis returns the seven-element diagnosis for every step in the
// run. Same auth path as GetRun (workspace membership via runInWorkspace).
// GET /api/workflow-runs/{id}/diagnosis
func (h *WorkflowRunHandler) GetRunDiagnosis(w http.ResponseWriter, r *http.Request) {
	run, ok := h.runInWorkspace(w, r)
	if !ok {
		return
	}
	steps, err := h.Queries.ListStepInstancesForRun(r.Context(), run.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list steps")
		return
	}
	transitions, err := h.Queries.ListStepTransitionsForRun(r.Context(), run.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list transitions")
		return
	}
	tasks, err := h.Queries.ListAgentTasksForRun(r.Context(), run.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list tasks")
		return
	}
	gates, err := h.Queries.ListGateRunsForRun(r.Context(), run.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list gate runs")
		return
	}
	writeJSON(w, http.StatusOK, buildRunDiagnosis(run, steps, transitions, tasks, gates))
}

// buildRunDiagnosis aggregates the four batch-loaded row sets into the
// per-step seven-element diagnosis. Pure (no I/O) so it is unit-tested
// directly with hand-built rows.
func buildRunDiagnosis(
	run db.WorkflowRun,
	steps []db.StepInstance,
	transitions []db.StepTransition,
	tasks []db.AgentTaskQueue,
	gates []db.GateRun,
) runDiagnosisDTO {
	taskByID := make(map[pgtype.UUID]db.AgentTaskQueue, len(tasks))
	for _, t := range tasks {
		taskByID[t.ID] = t
	}
	gatesByStep := make(map[pgtype.UUID][]db.GateRun)
	for _, g := range gates {
		gatesByStep[g.StepInstanceID] = append(gatesByStep[g.StepInstanceID], g)
	}
	transByStep := make(map[pgtype.UUID][]db.StepTransition)
	for _, tr := range transitions {
		transByStep[tr.StepInstanceID] = append(transByStep[tr.StepInstanceID], tr)
	}

	out := runDiagnosisDTO{
		RunID:  uuidToString(run.ID),
		Status: run.Status,
		Steps:  make([]stepDiagnosisDTO, 0, len(steps)),
	}
	for _, s := range steps {
		diag := stepDiagnosisDTO{
			StepID:      uuidToString(s.ID),
			NodeKey:     s.NodeKey,
			RunID:       uuidToString(s.RunID),
			TaskID:      uuidPtr(s.AgentTaskID),
			AgentID:     uuidPtr(s.AgentID),
			Attempt:     s.Attempt,
			FinalStatus: s.Status,
			Transitions: transitionsToDTO(transByStep[s.ID]),
		}
		task, hasTask := taskByID[s.AgentTaskID]
		if hasTask && task.MaxAttempts > 0 {
			ma := task.MaxAttempts
			diag.MaxAttempts = &ma
		}
		classifyStepDiagnosis(&diag, s, transByStep[s.ID], gatesByStep[s.ID], task, hasTask)
		out.Steps = append(out.Steps, diag)
	}
	return out
}

// classifyStepDiagnosis fills OK / FailureType / Reason / Output. Reason is
// assembled from every available signal (task.FailureReason, task.Error,
// gate stderr, transition trigger) so the diagnosis answers "why did this
// step fail" without the operator cross-referencing four tables by hand.
func classifyStepDiagnosis(
	diag *stepDiagnosisDTO,
	s db.StepInstance,
	transitions []db.StepTransition,
	gates []db.GateRun,
	task db.AgentTaskQueue,
	hasTask bool,
) {
	var reasons []string

	if hasTask {
		if task.FailureReason.Valid && task.FailureReason.String != "" {
			reasons = append(reasons, task.FailureReason.String)
		}
		if task.Error.Valid && task.Error.String != "" {
			reasons = append(reasons, "task error: "+task.Error.String)
		}
	}

	// A finalized block/error gate_run is the strongest failure signal —
	// attach its output (carries stdout/stderr) and surface the stderr line.
	for _, g := range gates {
		if g.Status != "block" && g.Status != "error" {
			continue
		}
		if diag.FailureType == "" {
			diag.FailureType = "gate_reject"
		}
		reasons = append(reasons, fmt.Sprintf("gate %s %s", g.GateType, g.Status))
		if diag.Output == nil && len(g.Output) > 0 {
			diag.Output = json.RawMessage(g.Output)
		}
		if stderr := extractStderr(g.Output); stderr != "" {
			reasons = append(reasons, "stderr: "+stderr)
		}
	}

	// Sweeper self-heal leaves a transition with trigger_by='sweeper' —
	// informational (the step was re-dispatched/reset by the P1-5 sweeper),
	// not itself a failure type. Surfaced in reason so the operator sees the
	// self-heal event alongside any real failure.
	for _, tr := range transitions {
		if tr.TriggerBy == "sweeper" {
			reasons = append(reasons, fmt.Sprintf("sweeper %s→%s reset", tr.FromStatus, tr.ToStatus))
			break
		}
	}

	// Terminal-status classification (values match step_instance CHECK).
	switch s.Status {
	case "failed":
		diag.OK = false
		if diag.FailureType == "" {
			diag.FailureType = "fail"
		}
	case "blocked":
		diag.OK = false
		if diag.FailureType == "" {
			diag.FailureType = "blocked"
		}
	case "rework":
		diag.OK = false
		if diag.FailureType == "" {
			diag.FailureType = "rework"
		}
	default:
		// pending/active/dispatched/running/passed/skipped — no failure.
		diag.OK = true
	}

	diag.Reason = strings.Join(reasons, "; ")
}

// extractStderr pulls the `stderr` string out of a gate_run output JSONB
// ({stdout, stderr, facts, dispositions, ...}). Returns "" on any parse
// failure or missing field — diagnosis must never break on a malformed row.
func extractStderr(output []byte) string {
	if len(output) == 0 {
		return ""
	}
	var parsed struct {
		Stderr string `json:"stderr"`
	}
	if json.Unmarshal(output, &parsed) != nil {
		return ""
	}
	return strings.TrimSpace(parsed.Stderr)
}

// transitionsToDTO converts step_transition rows to the timeline DTO shared
// with GetRun's AC4 trace.
func transitionsToDTO(transitions []db.StepTransition) []workflowTransitionDTO {
	out := make([]workflowTransitionDTO, 0, len(transitions))
	for _, tr := range transitions {
		out = append(out, workflowTransitionDTO{
			ID:             uuidToString(tr.ID),
			StepInstanceID: uuidToString(tr.StepInstanceID),
			FromStatus:     tr.FromStatus,
			ToStatus:       tr.ToStatus,
			Attempt:        tr.Attempt,
			TriggerBy:      tr.TriggerBy,
			Payload:        json.RawMessage(tr.Payload),
			CreatedAt:      timestampToString(tr.CreatedAt),
		})
	}
	return out
}
