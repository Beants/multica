package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/workflow"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// workflow_run.go — human-facing workflow run API (R5): workspace-scoped run
// list/detail plus the acceptance decision endpoints. Auth is session/PAT via
// the workspace-member middleware (never mat_ — runs are an operator
// surface). The acceptance reviewer is the CURRENT USER's member row, per the
// API contract (design.md §3); the engine restamps acceptance.reviewer_id
// with that decider on the guarded write.

// WorkflowRunHandler serves /api/workflow-runs*. Kept separate from Handler
// so the fork adds zero fields to the upstream struct; router_workflow.go
// wires it.
type WorkflowRunHandler struct {
	Queries *db.Queries
	Engine  *workflow.Engine
}

func NewWorkflowRunHandler(q *db.Queries, eng *workflow.Engine) *WorkflowRunHandler {
	return &WorkflowRunHandler{Queries: q, Engine: eng}
}

// ---------------------------------------------------------------------------
// DTOs
// ---------------------------------------------------------------------------

type workflowRunDTO struct {
	ID            string  `json:"id"`
	TemplateID    string  `json:"template_id"`
	Status        string  `json:"status"`
	SourceType    string  `json:"source_type"`
	SourceID      *string `json:"source_id,omitempty"`
	IntakeIssueID *string `json:"intake_issue_id,omitempty"`
	StartedAt     string  `json:"started_at"`
	CompletedAt   *string `json:"completed_at,omitempty"`
	UpdatedAt     string  `json:"updated_at"`
}

type workflowStepDTO struct {
	ID          string          `json:"id"`
	NodeKey     string          `json:"node_key"`
	Status      string          `json:"status"`
	Attempt     int32           `json:"attempt"`
	AgentID     *string         `json:"agent_id,omitempty"`
	AgentTaskID *string         `json:"agent_task_id,omitempty"`
	IssueID     *string         `json:"issue_id,omitempty"`
	ExitFields  json.RawMessage `json:"exit_fields,omitempty"`
	StartedAt   *string         `json:"started_at,omitempty"`
	FinishedAt  *string         `json:"finished_at,omitempty"`
	CreatedAt   string          `json:"created_at"`
}

type workflowRunSubmissionDTO struct {
	ID             string          `json:"id"`
	StepInstanceID string          `json:"step_instance_id"`
	Status         string          `json:"status"`
	Gaps           json.RawMessage `json:"gaps,omitempty"`
	Artifacts      json.RawMessage `json:"artifacts,omitempty"`
	ExitFields     json.RawMessage `json:"exit_fields,omitempty"`
	RawSummary     *string         `json:"raw_summary,omitempty"`
	CreatedAt      string          `json:"created_at"`
}

type workflowAcceptanceDTO struct {
	ID              string          `json:"id"`
	StepInstanceID  string          `json:"step_instance_id"`
	Status          string          `json:"status"`
	ReviewerID      *string         `json:"reviewer_id,omitempty"`
	DecidedAt       *string         `json:"decided_at,omitempty"`
	RejectReason    *string         `json:"reject_reason,omitempty"`
	RejectToNodeKey *string         `json:"reject_to_node_key,omitempty"`
	ReworkContext   json.RawMessage `json:"rework_context,omitempty"`
	CreatedAt       string          `json:"created_at"`
}

type workflowTransitionDTO struct {
	ID             string          `json:"id"`
	StepInstanceID string          `json:"step_instance_id"`
	FromStatus     string          `json:"from_status"`
	ToStatus       string          `json:"to_status"`
	Attempt        int32           `json:"attempt"`
	TriggerBy      string          `json:"trigger_by"`
	Payload        json.RawMessage `json:"payload,omitempty"`
	CreatedAt      string          `json:"created_at"`
}

// workflowRunDetailDTO is the AC4 trace view: the run plus every artifact
// the state machine produced, including the frozen template snapshot the
// steps reference by node_key.
type workflowRunDetailDTO struct {
	workflowRunDTO
	TemplateSnapshot json.RawMessage            `json:"template_snapshot"`
	Steps            []workflowStepDTO          `json:"steps"`
	Submissions      []workflowRunSubmissionDTO `json:"submissions"`
	Verdicts         []verdictResponse          `json:"verdicts"`
	Acceptances      []workflowAcceptanceDTO    `json:"acceptances"`
	Transitions      []workflowTransitionDTO    `json:"transitions"`
}

func workflowRunToDTO(run db.WorkflowRun) workflowRunDTO {
	return workflowRunDTO{
		ID:            uuidToString(run.ID),
		TemplateID:    uuidToString(run.TemplateID),
		Status:        run.Status,
		SourceType:    run.SourceType,
		SourceID:      textToPtr(run.SourceID),
		IntakeIssueID: uuidPtr(run.IntakeIssueID),
		StartedAt:     timestampToString(run.StartedAt),
		CompletedAt:   tsPtr(run.CompletedAt),
		UpdatedAt:     timestampToString(run.UpdatedAt),
	}
}

func tsPtr(t pgtype.Timestamptz) *string {
	if !t.Valid {
		return nil
	}
	s := timestampToString(t)
	return &s
}

func uuidPtr(u pgtype.UUID) *string {
	if !u.Valid {
		return nil
	}
	s := uuidToString(u)
	return &s
}

// ---------------------------------------------------------------------------
// GET /api/workflow-runs + GET /api/workflow-runs/{id}
// ---------------------------------------------------------------------------

// ListRuns lists the workspace's runs, newest first.
func (h *WorkflowRunHandler) ListRuns(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := parseUUIDOrBadRequest(w, ctxWorkspaceID(r.Context()), "workspace_id")
	if !ok {
		return
	}
	runs, err := h.Queries.ListWorkflowRuns(r.Context(), workspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list workflow runs")
		return
	}
	out := make([]workflowRunDTO, 0, len(runs))
	for _, run := range runs {
		out = append(out, workflowRunToDTO(run))
	}
	writeJSON(w, http.StatusOK, out)
}

// GetRun returns the run detail: run + frozen snapshot + steps + submissions
// + verdicts + acceptances + the step_transition timeline (AC4).
func (h *WorkflowRunHandler) GetRun(w http.ResponseWriter, r *http.Request) {
	run, ok := h.runInWorkspace(w, r)
	if !ok {
		return
	}
	steps, err := h.Queries.ListStepInstancesForRun(r.Context(), run.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list steps")
		return
	}
	submissions, err := h.Queries.ListSubmissionsForRun(r.Context(), run.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list submissions")
		return
	}
	verdicts, err := h.Queries.ListVerdictsForRun(r.Context(), run.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list verdicts")
		return
	}
	acceptances, err := h.Queries.ListAcceptancesForRun(r.Context(), run.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list acceptances")
		return
	}
	transitions, err := h.Queries.ListStepTransitionsForRun(r.Context(), run.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list transitions")
		return
	}

	detail := workflowRunDetailDTO{
		workflowRunDTO:   workflowRunToDTO(run),
		TemplateSnapshot: run.TemplateSnapshot,
		Steps:            make([]workflowStepDTO, 0, len(steps)),
		Submissions:      make([]workflowRunSubmissionDTO, 0, len(submissions)),
		Verdicts:         make([]verdictResponse, 0, len(verdicts)),
		Acceptances:      make([]workflowAcceptanceDTO, 0, len(acceptances)),
		Transitions:      make([]workflowTransitionDTO, 0, len(transitions)),
	}
	for _, s := range steps {
		detail.Steps = append(detail.Steps, workflowStepDTO{
			ID:          uuidToString(s.ID),
			NodeKey:     s.NodeKey,
			Status:      s.Status,
			Attempt:     s.Attempt,
			AgentID:     uuidPtr(s.AgentID),
			AgentTaskID: uuidPtr(s.AgentTaskID),
			IssueID:     uuidPtr(s.IssueID),
			ExitFields:  s.ExitFields,
			StartedAt:   tsPtr(s.StartedAt),
			FinishedAt:  tsPtr(s.FinishedAt),
			CreatedAt:   timestampToString(s.CreatedAt),
		})
	}
	for _, s := range submissions {
		detail.Submissions = append(detail.Submissions, workflowRunSubmissionDTO{
			ID:             uuidToString(s.ID),
			StepInstanceID: uuidToString(s.StepInstanceID),
			Status:         s.Status,
			Gaps:           s.Gaps,
			Artifacts:      s.Artifacts,
			ExitFields:     s.ExitFields,
			RawSummary:     textToPtr(s.RawSummary),
			CreatedAt:      timestampToString(s.CreatedAt),
		})
	}
	for _, v := range verdicts {
		detail.Verdicts = append(detail.Verdicts, verdictToResponse(v))
	}
	for _, a := range acceptances {
		detail.Acceptances = append(detail.Acceptances, workflowAcceptanceDTO{
			ID:              uuidToString(a.ID),
			StepInstanceID:  uuidToString(a.StepInstanceID),
			Status:          a.Status,
			ReviewerID:      uuidPtr(a.ReviewerID),
			DecidedAt:       tsPtr(a.DecidedAt),
			RejectReason:    textToPtr(a.RejectReason),
			RejectToNodeKey: textToPtr(a.RejectToNodeKey),
			ReworkContext:   a.ReworkContext,
			CreatedAt:       timestampToString(a.CreatedAt),
		})
	}
	for _, t := range transitions {
		detail.Transitions = append(detail.Transitions, workflowTransitionDTO{
			ID:             uuidToString(t.ID),
			StepInstanceID: uuidToString(t.StepInstanceID),
			FromStatus:     t.FromStatus,
			ToStatus:       t.ToStatus,
			Attempt:        t.Attempt,
			TriggerBy:      t.TriggerBy,
			Payload:        t.Payload,
			CreatedAt:      timestampToString(t.CreatedAt),
		})
	}
	writeJSON(w, http.StatusOK, detail)
}

// ---------------------------------------------------------------------------
// POST /api/workflow-runs/{id}/acceptance/approve + /reject
// ---------------------------------------------------------------------------

type acceptanceDecisionResponse struct {
	RunID        string `json:"run_id"`
	AcceptanceID string `json:"acceptance_id"`
	Status       string `json:"status"`
}

// ApproveAcceptance decides the run's pending acceptance as approved; the
// acceptance step passes and the chain advances. The decider is the current
// user's member row (design.md §3 "reviewer = current user member").
func (h *WorkflowRunHandler) ApproveAcceptance(w http.ResponseWriter, r *http.Request) {
	run, acc, member, ok := h.pendingAcceptance(w, r)
	if !ok {
		return
	}
	if err := h.Engine.ApproveAcceptance(r.Context(), run.ID, acc.ID, member.ID); err != nil {
		writeAcceptanceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, acceptanceDecisionResponse{
		RunID:        uuidToString(run.ID),
		AcceptanceID: uuidToString(acc.ID),
		Status:       "approved",
	})
}

type rejectAcceptanceRequest struct {
	RejectToNodeKey string `json:"reject_to_node_key"`
	Reason          string `json:"reason"`
}

// RejectAcceptance decides the run's pending acceptance as rejected and
// starts targeted rework to reject_to_node_key (design.md §4.4).
func (h *WorkflowRunHandler) RejectAcceptance(w http.ResponseWriter, r *http.Request) {
	var req rejectAcceptanceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.RejectToNodeKey == "" {
		writeError(w, http.StatusBadRequest, "reject_to_node_key is required")
		return
	}
	if req.Reason == "" {
		writeError(w, http.StatusBadRequest, "reason is required")
		return
	}
	run, acc, member, ok := h.pendingAcceptance(w, r)
	if !ok {
		return
	}
	if err := h.Engine.RejectAcceptance(r.Context(), run.ID, acc.ID, member.ID, req.RejectToNodeKey, req.Reason); err != nil {
		writeAcceptanceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, acceptanceDecisionResponse{
		RunID:        uuidToString(run.ID),
		AcceptanceID: uuidToString(acc.ID),
		Status:       "rejected",
	})
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

// runInWorkspace resolves {id} to a run inside the request's workspace —
// the workspace-scoping chokepoint for every run endpoint.
func (h *WorkflowRunHandler) runInWorkspace(w http.ResponseWriter, r *http.Request) (db.WorkflowRun, bool) {
	workspaceID, ok := parseUUIDOrBadRequest(w, ctxWorkspaceID(r.Context()), "workspace_id")
	if !ok {
		return db.WorkflowRun{}, false
	}
	runID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "id")
	if !ok {
		return db.WorkflowRun{}, false
	}
	run, err := h.Queries.GetWorkflowRunInWorkspace(r.Context(), db.GetWorkflowRunInWorkspaceParams{
		ID: runID, WorkspaceID: workspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "workflow run not found")
			return db.WorkflowRun{}, false
		}
		writeError(w, http.StatusInternalServerError, "failed to load workflow run")
		return db.WorkflowRun{}, false
	}
	return run, true
}

// pendingAcceptance resolves the run + its single pending acceptance + the
// calling member, the shared prelude of approve/reject. A run without a
// pending acceptance is a 409 (it is not waiting for a decision), not a 404 —
// the run itself exists.
func (h *WorkflowRunHandler) pendingAcceptance(w http.ResponseWriter, r *http.Request) (db.WorkflowRun, db.Acceptance, db.Member, bool) {
	run, ok := h.runInWorkspace(w, r)
	if !ok {
		return db.WorkflowRun{}, db.Acceptance{}, db.Member{}, false
	}
	member, ok := ctxMember(r.Context())
	if !ok {
		writeError(w, http.StatusForbidden, "workspace membership required")
		return db.WorkflowRun{}, db.Acceptance{}, db.Member{}, false
	}
	acc, err := h.Queries.GetPendingAcceptanceByRun(r.Context(), run.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusConflict, "run is not waiting for an acceptance decision")
			return db.WorkflowRun{}, db.Acceptance{}, db.Member{}, false
		}
		writeError(w, http.StatusInternalServerError, "failed to load pending acceptance")
		return db.WorkflowRun{}, db.Acceptance{}, db.Member{}, false
	}
	return run, acc, member, true
}

// writeAcceptanceError maps engine acceptance sentinels to HTTP codes.
func writeAcceptanceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, workflow.ErrRunNotFound):
		writeError(w, http.StatusNotFound, "workflow run not found")
	case errors.Is(err, workflow.ErrAcceptanceNotFound):
		writeError(w, http.StatusNotFound, "acceptance not found for this run")
	case errors.Is(err, workflow.ErrAcceptanceConflict):
		writeError(w, http.StatusConflict, "acceptance already decided")
	case errors.Is(err, workflow.ErrRunNotActive):
		writeError(w, http.StatusConflict, "run is not waiting for an acceptance decision")
	case errors.Is(err, workflow.ErrReworkTargetUnknown):
		writeError(w, http.StatusBadRequest, "reject_to_node_key does not name a node that ran in this run")
	case errors.Is(err, workflow.ErrReworkTargetActive):
		writeError(w, http.StatusConflict, "rework target node still has an in-flight step")
	default:
		writeError(w, http.StatusInternalServerError, "workflow engine error: "+err.Error())
	}
}
