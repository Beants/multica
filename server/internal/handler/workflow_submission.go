package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/workflow"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// workflow_submission.go — agent-facing workflow API (R4/R10): submission +
// verdict writes and reads, authenticated by mat_ task tokens (the auth
// middleware stamps X-Task-ID / X-Agent-ID / X-Workspace-ID from the token
// row; see middleware/auth.go:73-97). Two trust rules live here:
//
//  1. task-token only: every endpoint rejects non-mat_ callers (403), and
//     the URL task id must equal the token's bound task id — an agent must
//     not post against another task's step.
//  2. verdict actor model (design.md §4.3): executor-role steps are judged
//     by the system-derived verdict, so an executor token writing a verdict
//     is a 403; only evaluator-role steps accept verdict writes.
//
// Exit-fields validation is dual-layer (R4): this file validates against the
// node's frozen schema before calling the engine (structured field errors),
// and the engine re-validates inside its write transaction.

// WorkflowHandler serves the /api/tasks/{id}/{submission,verdict,step-context}
// routes. Kept separate from Handler so the fork adds zero fields to the
// upstream struct; router_workflow.go wires it.
type WorkflowHandler struct {
	Queries *db.Queries
	Engine  *workflow.Engine
}

func NewWorkflowHandler(q *db.Queries, eng *workflow.Engine) *WorkflowHandler {
	return &WorkflowHandler{Queries: q, Engine: eng}
}

// taskTokenActor extracts the mat_-bound task id stamped by the auth
// middleware. Non-task-token callers (PAT, JWT, cloud PAT) get 403: these
// endpoints are agent-only surfaces. The agent id is not returned: every
// endpoint authorizes on the task binding alone (the token IS the task's
// credential), so a step lookup never needs the caller's agent id.
func taskTokenActor(w http.ResponseWriter, r *http.Request) (taskID pgtype.UUID, ok bool) {
	if r.Header.Get("X-Actor-Source") != "task_token" {
		writeError(w, http.StatusForbidden, "a task token (mat_) is required")
		return pgtype.UUID{}, false
	}
	taskID, err := util.ParseUUID(r.Header.Get("X-Task-ID"))
	if err != nil {
		writeError(w, http.StatusForbidden, "task token is not bound to a task")
		return pgtype.UUID{}, false
	}
	return taskID, true
}

// urlTaskMatchesToken enforces URL {id} == the token's bound task id.
func urlTaskMatchesToken(w http.ResponseWriter, r *http.Request, taskID pgtype.UUID) bool {
	urlID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "id")
	if !ok {
		return false
	}
	if urlID != taskID {
		writeError(w, http.StatusForbidden, "task token is bound to a different task")
		return false
	}
	return true
}

// ---------------------------------------------------------------------------
// POST /api/tasks/{id}/submission
// ---------------------------------------------------------------------------

type createSubmissionRequest struct {
	Status         string          `json:"status"`
	Gaps           json.RawMessage `json:"gaps,omitempty"`
	Artifacts      json.RawMessage `json:"artifacts,omitempty"`
	ExitFields     map[string]any  `json:"exit_fields,omitempty"`
	RawSummary     string          `json:"raw_summary,omitempty"`
	IdempotencyKey string          `json:"idempotency_key,omitempty"`
}

type submissionResponse struct {
	ID             string          `json:"id"`
	StepInstanceID string          `json:"step_instance_id"`
	TaskID         string          `json:"task_id,omitempty"`
	Status         string          `json:"status"`
	Gaps           json.RawMessage `json:"gaps,omitempty"`
	Artifacts      json.RawMessage `json:"artifacts,omitempty"`
	ExitFields     json.RawMessage `json:"exit_fields,omitempty"`
	RawSummary     *string         `json:"raw_summary,omitempty"`
	IdempotencyKey *string         `json:"idempotency_key,omitempty"`
	Created        bool            `json:"created"`
	CreatedAt      string          `json:"created_at"`
}

func submissionToResponse(sub db.Submission, created bool) submissionResponse {
	return submissionResponse{
		ID:             uuidToString(sub.ID),
		StepInstanceID: uuidToString(sub.StepInstanceID),
		TaskID:         uuidToString(sub.TaskID),
		Status:         sub.Status,
		Gaps:           sub.Gaps,
		Artifacts:      sub.Artifacts,
		ExitFields:     sub.ExitFields,
		RawSummary:     textToPtr(sub.RawSummary),
		IdempotencyKey: textToPtr(sub.IdempotencyKey),
		Created:        created,
		CreatedAt:      timestampToString(sub.CreatedAt),
	}
}

// CreateSubmission records the agent's work product for the step bound to
// its task. Idempotent via idempotency_key: a replay returns the original
// row with created=false (blueprint §8.2). Missing required exit fields are
// a structured 422; unknown fields pass through untouched (D-9).
func (h *WorkflowHandler) CreateSubmission(w http.ResponseWriter, r *http.Request) {
	taskID, ok := taskTokenActor(w, r)
	if !ok || !urlTaskMatchesToken(w, r, taskID) {
		return
	}
	var req createSubmissionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	switch req.Status {
	case workflow.SubmissionDone, workflow.SubmissionDoneWithConcerns,
		workflow.SubmissionBlocked, workflow.SubmissionNeedsContext:
	default:
		writeError(w, http.StatusBadRequest, "status must be one of DONE, DONE_WITH_CONCERNS, BLOCKED, NEEDS_CONTEXT")
		return
	}

	// Layer-1 exit-fields validation against the frozen node schema; the
	// engine re-checks inside its write tx (dual layer, design.md §1).
	_, node, err := h.Engine.LoadStepNode(r.Context(), taskID)
	if err != nil {
		writeWorkflowError(w, err)
		return
	}
	if fieldErrs := workflow.ValidateExitFieldsForStatus(req.Status, node.Config.ExitFields, req.ExitFields); len(fieldErrs) > 0 {
		writeExitFieldsError(w, http.StatusUnprocessableEntity, fieldErrs)
		return
	}
	// Layer-1 D-11: artifacts carry durable references only (PR URL, branch,
	// attachment ID) — local/workdir paths are garbage collected (inventory
	// 6.2). The engine re-checks inside its write path.
	if fieldErrs := workflow.ValidateArtifacts(req.Artifacts); len(fieldErrs) > 0 {
		writeArtifactsError(w, http.StatusBadRequest, fieldErrs)
		return
	}

	sub, created, err := h.Engine.RecordSubmission(r.Context(), taskID, workflow.SubmissionInput{
		Status:         req.Status,
		Gaps:           req.Gaps,
		Artifacts:      req.Artifacts,
		ExitFields:     req.ExitFields,
		RawSummary:     req.RawSummary,
		IdempotencyKey: req.IdempotencyKey,
	})
	if err != nil {
		writeWorkflowError(w, err)
		return
	}
	status := http.StatusCreated
	if !created {
		status = http.StatusOK
	}
	writeJSON(w, status, submissionToResponse(sub, created))
}

// ---------------------------------------------------------------------------
// POST /api/tasks/{id}/verdict + GET /api/tasks/{id}/verdict
// ---------------------------------------------------------------------------

type createVerdictRequest struct {
	Result     string          `json:"result"`
	RootCause  string          `json:"root_cause,omitempty"`
	Confidence *float64        `json:"confidence,omitempty"`
	Evidence   json.RawMessage `json:"evidence,omitempty"`
	ExitFields map[string]any  `json:"exit_fields,omitempty"`
}

type verdictResponse struct {
	ID             string          `json:"id"`
	SubmissionID   string          `json:"submission_id"`
	StepInstanceID string          `json:"step_instance_id"`
	Result         string          `json:"result"`
	RootCause      *string         `json:"root_cause,omitempty"`
	Confidence     *float64        `json:"confidence,omitempty"`
	Evidence       json.RawMessage `json:"evidence,omitempty"`
	VerdictBy      string          `json:"verdict_by"`
	CreatedAt      string          `json:"created_at"`
}

func verdictToResponse(v db.Verdict) verdictResponse {
	resp := verdictResponse{
		ID:             uuidToString(v.ID),
		SubmissionID:   uuidToString(v.SubmissionID),
		StepInstanceID: uuidToString(v.StepInstanceID),
		Result:         v.Result,
		RootCause:      textToPtr(v.RootCause),
		Evidence:       v.Evidence,
		VerdictBy:      v.VerdictBy,
		CreatedAt:      timestampToString(v.CreatedAt),
	}
	if f, err := v.Confidence.Float64Value(); err == nil && f.Valid {
		resp.Confidence = &f.Float64
	}
	return resp
}

// CreateVerdict writes the evaluator's verdict for its own step (verdict
// actor model: executor-role tokens get 403). When the step has no
// submission yet, the engine auto-creates a minimal one in the same
// transaction — through the same exit-fields validation, so a node with
// required exit fields rejects a verdict that omits them (400 with the
// structured field list).
func (h *WorkflowHandler) CreateVerdict(w http.ResponseWriter, r *http.Request) {
	taskID, ok := taskTokenActor(w, r)
	if !ok || !urlTaskMatchesToken(w, r, taskID) {
		return
	}
	var req createVerdictRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	switch req.Result {
	case workflow.VerdictPass, workflow.VerdictFail, workflow.VerdictBlocked:
	default:
		writeError(w, http.StatusBadRequest, "result must be one of pass, fail, blocked")
		return
	}

	step, node, err := h.Engine.LoadStepNode(r.Context(), taskID)
	if err != nil {
		writeWorkflowError(w, err)
		return
	}
	if node.Config.EffectiveRole() != workflow.RoleEvaluator {
		writeError(w, http.StatusForbidden, "verdict writes require an evaluator-role step")
		return
	}
	// Layer-1: only the auto-create path needs exit fields — when the step
	// already carries a submission, the schema was validated at submission
	// time.
	if _, err := h.Queries.GetSubmissionByStepInstance(r.Context(), step.ID); err != nil {
		if fieldErrs := workflow.ValidateExitFields(node.Config.ExitFields, req.ExitFields); len(fieldErrs) > 0 {
			writeExitFieldsError(w, http.StatusBadRequest, fieldErrs)
			return
		}
	}

	verdict, err := h.Engine.RecordVerdict(r.Context(), taskID, workflow.VerdictInput{
		Result:     req.Result,
		RootCause:  req.RootCause,
		Confidence: req.Confidence,
		Evidence:   req.Evidence,
		ExitFields: req.ExitFields,
	})
	if err != nil {
		writeWorkflowError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, verdictToResponse(verdict))
}

// GetVerdict returns the verdict attached to the caller's step (404 until a
// verdict exists — the CLI polls this after submitting).
func (h *WorkflowHandler) GetVerdict(w http.ResponseWriter, r *http.Request) {
	taskID, ok := taskTokenActor(w, r)
	if !ok || !urlTaskMatchesToken(w, r, taskID) {
		return
	}
	step, _, err := h.Engine.LoadStepNode(r.Context(), taskID)
	if err != nil {
		writeWorkflowError(w, err)
		return
	}
	verdict, err := h.Queries.GetVerdictByStepInstance(r.Context(), step.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, "no verdict for this step yet")
		return
	}
	writeJSON(w, http.StatusOK, verdictToResponse(verdict))
}

// GetStepContext returns the node context for the caller's step (R10):
// instructions, the immediate upstream node's exit fields, and this node's
// exit-fields schema.
func (h *WorkflowHandler) GetStepContext(w http.ResponseWriter, r *http.Request) {
	taskID, ok := taskTokenActor(w, r)
	if !ok || !urlTaskMatchesToken(w, r, taskID) {
		return
	}
	sc, err := h.Engine.GetStepContext(r.Context(), taskID)
	if err != nil {
		writeWorkflowError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sc)
}

// ---------------------------------------------------------------------------
// Error mapping
// ---------------------------------------------------------------------------

// exitFieldsErrorBody is the structured validation failure (AC3): clients
// render per-field errors without parsing prose.
type exitFieldsErrorBody struct {
	Error  string                `json:"error"`
	Fields []workflow.FieldError `json:"fields"`
}

func writeExitFieldsError(w http.ResponseWriter, status int, fields []workflow.FieldError) {
	writeJSON(w, status, exitFieldsErrorBody{
		Error:  "exit_fields validation failed",
		Fields: fields,
	})
}

// artifactsErrorBody is the structured D-11 rejection: each field entry
// names the JSON path of an offending local-path string inside artifacts.
type artifactsErrorBody struct {
	Error  string                `json:"error"`
	Fields []workflow.FieldError `json:"fields"`
}

func writeArtifactsError(w http.ResponseWriter, status int, fields []workflow.FieldError) {
	writeJSON(w, status, artifactsErrorBody{
		Error:  "artifacts validation failed",
		Fields: fields,
	})
}

// writeWorkflowError maps engine sentinel errors to HTTP codes.
func writeWorkflowError(w http.ResponseWriter, err error) {
	var validationErr *workflow.ExitFieldsValidationError
	var artifactsErr *workflow.ArtifactsValidationError
	switch {
	case errors.As(err, &validationErr):
		writeExitFieldsError(w, http.StatusUnprocessableEntity, validationErr.Fields)
	case errors.As(err, &artifactsErr):
		writeArtifactsError(w, http.StatusBadRequest, artifactsErr.Fields)
	case errors.Is(err, workflow.ErrStepNotFound):
		writeError(w, http.StatusNotFound, "no workflow step is bound to this task")
	case errors.Is(err, workflow.ErrNotEvaluatorStep):
		writeError(w, http.StatusForbidden, "verdict writes require an evaluator-role step")
	case errors.Is(err, workflow.ErrSubmissionExists):
		writeError(w, http.StatusConflict, "step already has a submission")
	case errors.Is(err, workflow.ErrVerdictExists):
		writeError(w, http.StatusConflict, "submission already has a verdict")
	case errors.Is(err, workflow.ErrStepTerminal):
		// Gate-W1 follow-up: writes on a terminal (passed/failed/skipped)
		// step are refused — the state machine would ignore their signal.
		writeError(w, http.StatusConflict, "step is terminal and no longer accepts writes")
	case errors.Is(err, workflow.ErrVerdictNotFound):
		writeError(w, http.StatusNotFound, "no verdict for this step yet")
	default:
		writeError(w, http.StatusInternalServerError, "workflow engine error: "+err.Error())
	}
}
