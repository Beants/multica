package handler

// workflow_gate_script.go — KI-4 management API for the workspace-scoped
// gate-script registry (migration 929/930). sqlc CRUD already exists in
// pkg/db/queries/workflow_gate_script.sql; this file exposes it over HTTP so
// workspaces can register/rotate the named scripts that gate nodes reference
// via node.config.gate_script_ref (the missing piece that caused the
// failActivation "gate_script_ref not found" in run d1b95048).
//
// Mirrors WorkflowRuleHandler's shape (struct + New + DTO + Create/List/
// Update/Delete) and WorkflowHookHandler's policy of never echoing back the
// large credential-like payload field: script_text is omitted from the DTO
// (callers get the checksum + length to detect drift; the body is fetched
// out-of-band when an editor actually needs it).

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Defaults + CHECK bounds mirror migration 929. sqlc's Create/Update Params
// force non-nullable int32 values, so the handler applies the column default
// when the caller omits the field and range-checks it when present (otherwise
// the DB CHECK surfaces as an opaque 500).
const (
	gateScriptDefaultMaxTimeoutSeconds int32 = 60
	gateScriptDefaultMaxOutputBytes    int32 = 1 << 20 // 1048576

	gateScriptMinTimeoutSeconds int32 = 1
	gateScriptMaxTimeoutSeconds int32 = 300
	gateScriptMinOutputBytes    int32 = 1024
	gateScriptMaxOutputBytes    int32 = 10 << 20 // 10485760
)

// WorkflowGateScriptHandler serves /api/workflow-gate-scripts*. Operator
// surface — every route sits behind RequireHumanActor in router_workflow.go
// (a gate script encodes team policy for what an agent may execute inline,
// so it is team governance, not agent self-service).
type WorkflowGateScriptHandler struct {
	Queries *db.Queries
}

func NewWorkflowGateScriptHandler(q *db.Queries) *WorkflowGateScriptHandler {
	return &WorkflowGateScriptHandler{Queries: q}
}

// workflowGateScriptDTO mirrors db.WorkflowGateScript minus script_text. A
// script body can be hundreds of KB; echoing it in list/detail responses
// bloats payloads that mostly need identity + drift detection. The checksum
// detects tampering and ScriptTextLength hints at size; the source itself is
// fetched through a dedicated endpoint when an editor needs it (mirrors how
// workflowHookDTO omits TokenHash).
type workflowGateScriptDTO struct {
	ID                string `json:"id"`
	WorkspaceID       string `json:"workspace_id"`
	Name              string `json:"name"`
	Language          string `json:"language"`
	Checksum          string `json:"checksum"`
	MaxTimeoutSeconds int32  `json:"max_timeout_seconds"`
	MaxOutputBytes    int32  `json:"max_output_bytes"`
	ScriptTextLength  int    `json:"script_text_length"`
	CreatedAt         string `json:"created_at"`
	UpdatedAt         string `json:"updated_at"`
}

func workflowGateScriptToDTO(s db.WorkflowGateScript) workflowGateScriptDTO {
	return workflowGateScriptDTO{
		ID:                uuidToString(s.ID),
		WorkspaceID:       uuidToString(s.WorkspaceID),
		Name:              s.Name,
		Language:          s.Language,
		Checksum:          s.Checksum,
		MaxTimeoutSeconds: s.MaxTimeoutSeconds,
		MaxOutputBytes:    s.MaxOutputBytes,
		ScriptTextLength:  len(s.ScriptText),
		CreatedAt:         timestampToString(s.CreatedAt),
		UpdatedAt:         timestampToString(s.UpdatedAt),
	}
}

type createWorkflowGateScriptRequest struct {
	Name              string `json:"name"`
	Language          string `json:"language"`
	ScriptText        string `json:"script_text"`
	MaxTimeoutSeconds *int32 `json:"max_timeout_seconds,omitempty"`
	MaxOutputBytes    *int32 `json:"max_output_bytes,omitempty"`
}

type updateWorkflowGateScriptRequest struct {
	Name              string `json:"name"`
	Language          string `json:"language"`
	ScriptText        string `json:"script_text"`
	MaxTimeoutSeconds *int32 `json:"max_timeout_seconds,omitempty"`
	MaxOutputBytes    *int32 `json:"max_output_bytes,omitempty"`
}

// checksumScriptText SHA-256 hashes the script source (hex digest). Stored on
// the row so audit can detect drift without re-reading the body — matches the
// hook token-hash convention but applied to script content.
func checksumScriptText(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// CreateGateScript POST /api/workflow-gate-scripts
func (h *WorkflowGateScriptHandler) CreateGateScript(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := parseUUIDOrBadRequest(w, ctxWorkspaceID(r.Context()), "workspace_id")
	if !ok {
		return
	}
	var req createWorkflowGateScriptRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := normalizeAndValidateGateScriptFields(&req.Name, &req.Language, req.ScriptText, req.MaxTimeoutSeconds, req.MaxOutputBytes); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	script, err := h.Queries.CreateWorkflowGateScript(r.Context(), db.CreateWorkflowGateScriptParams{
		WorkspaceID:       workspaceID,
		Name:              req.Name,
		Language:          req.Language,
		ScriptText:        req.ScriptText,
		Checksum:          checksumScriptText(req.ScriptText),
		MaxTimeoutSeconds: resolveGateScriptTimeout(req.MaxTimeoutSeconds),
		MaxOutputBytes:    resolveGateScriptOutputBytes(req.MaxOutputBytes),
	})
	if err != nil {
		// (workspace_id, name) uniqueness is enforced by migration 930's
		// concurrent unique index; a collision would surface here as a 23505
		// error. Treating it as 500 keeps the contract simple — clients that
		// need a friendlier 409 can map pgconn.PgError.Code later.
		slog.Error("workflow gate script: create failed", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create gate script")
		return
	}
	writeJSON(w, http.StatusCreated, workflowGateScriptToDTO(script))
}

// ListGateScripts GET /api/workflow-gate-scripts
func (h *WorkflowGateScriptHandler) ListGateScripts(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := parseUUIDOrBadRequest(w, ctxWorkspaceID(r.Context()), "workspace_id")
	if !ok {
		return
	}
	scripts, err := h.Queries.ListWorkflowGateScripts(r.Context(), workspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list gate scripts")
		return
	}
	out := make([]workflowGateScriptDTO, 0, len(scripts))
	for _, s := range scripts {
		out = append(out, workflowGateScriptToDTO(s))
	}
	writeJSON(w, http.StatusOK, out)
}

// UpdateGateScript PUT /api/workflow-gate-scripts/{id}
//
// workspace_id is part of the sqlc WHERE clause, so a leaked id from another
// workspace updates zero rows and surfaces as a 404 (no cross-scope update).
func (h *WorkflowGateScriptHandler) UpdateGateScript(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := parseUUIDOrBadRequest(w, ctxWorkspaceID(r.Context()), "workspace_id")
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "id")
	if !ok {
		return
	}
	var req updateWorkflowGateScriptRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := normalizeAndValidateGateScriptFields(&req.Name, &req.Language, req.ScriptText, req.MaxTimeoutSeconds, req.MaxOutputBytes); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	script, err := h.Queries.UpdateWorkflowGateScript(r.Context(), db.UpdateWorkflowGateScriptParams{
		ID:                id,
		WorkspaceID:       workspaceID,
		Name:              req.Name,
		Language:          req.Language,
		ScriptText:        req.ScriptText,
		Checksum:          checksumScriptText(req.ScriptText),
		MaxTimeoutSeconds: resolveGateScriptTimeout(req.MaxTimeoutSeconds),
		MaxOutputBytes:    resolveGateScriptOutputBytes(req.MaxOutputBytes),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "gate script not found")
			return
		}
		slog.Error("workflow gate script: update failed", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to update gate script")
		return
	}
	writeJSON(w, http.StatusOK, workflowGateScriptToDTO(script))
}

// DeleteGateScript DELETE /api/workflow-gate-scripts/{id}
func (h *WorkflowGateScriptHandler) DeleteGateScript(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := parseUUIDOrBadRequest(w, ctxWorkspaceID(r.Context()), "workspace_id")
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "id")
	if !ok {
		return
	}
	n, err := h.Queries.DeleteWorkflowGateScript(r.Context(), db.DeleteWorkflowGateScriptParams{
		ID: id, WorkspaceID: workspaceID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete gate script")
		return
	}
	if n == 0 {
		writeError(w, http.StatusNotFound, "gate script not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// normalizeAndValidateGateScriptFields trims + validates the mutable fields
// shared by create/update. name + script_text are required; language defaults
// to 'shell' (migration 929 column default) when omitted; the timeout/output
// pointers are only range-checked when the caller supplied them, otherwise
// resolveGateScript{Timeout,OutputBytes} applies the column default.
func normalizeAndValidateGateScriptFields(name *string, language *string, scriptText string, maxTimeout *int32, maxOutput *int32) error {
	*name = strings.TrimSpace(*name)
	if *name == "" {
		return errors.New("name is required")
	}
	if scriptText == "" {
		return errors.New("script_text is required")
	}
	*language = strings.TrimSpace(*language)
	if *language == "" {
		*language = "shell"
	}
	if *language != "shell" && *language != "python3" {
		return errors.New("language must be one of shell/python3")
	}
	if maxTimeout != nil && (*maxTimeout < gateScriptMinTimeoutSeconds || *maxTimeout > gateScriptMaxTimeoutSeconds) {
		return errors.New("max_timeout_seconds must be between 1 and 300")
	}
	if maxOutput != nil && (*maxOutput < gateScriptMinOutputBytes || *maxOutput > gateScriptMaxOutputBytes) {
		return errors.New("max_output_bytes must be between 1024 and 10485760")
	}
	return nil
}

func resolveGateScriptTimeout(v *int32) int32 {
	if v == nil {
		return gateScriptDefaultMaxTimeoutSeconds
	}
	return *v
}

func resolveGateScriptOutputBytes(v *int32) int32 {
	if v == nil {
		return gateScriptDefaultMaxOutputBytes
	}
	return *v
}
