package handler

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/workflow"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// workflow_template.go — human-facing workflow template API (R6): draft CRUD
// plus the publish/archive lifecycle, authenticated by session/PAT through the
// workspace-member middleware (never mat_ — templates are a management
// surface). All publish semantics (snapshot freeze, selector→UUID freezing,
// evaluator≠executor separation, version lifecycle) live in
// workflow.TemplateService; this file is the transport layer: decode,
// workspace-scope, map service sentinels to HTTP codes.

// WorkflowTemplateHandler serves /api/workflow-templates*. Kept separate
// from Handler so the fork adds zero fields to the upstream struct;
// router_workflow.go wires it.
type WorkflowTemplateHandler struct {
	Queries   *db.Queries
	Templates *workflow.TemplateService
}

func NewWorkflowTemplateHandler(q *db.Queries, templates *workflow.TemplateService) *WorkflowTemplateHandler {
	return &WorkflowTemplateHandler{Queries: q, Templates: templates}
}

// ---------------------------------------------------------------------------
// DTOs — edges are keyed by node_key (row UUIDs are an internal detail)
// ---------------------------------------------------------------------------

type workflowTemplateDTO struct {
	ID          string `json:"id"`
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Version     int32  `json:"version"`
	Status      string `json:"status"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

type workflowNodeDTO struct {
	ID       string          `json:"id"`
	NodeKey  string          `json:"node_key"`
	Type     string          `json:"type"`
	Name     string          `json:"name"`
	Config   json.RawMessage `json:"config"`
	Position json.RawMessage `json:"position,omitempty"`
}

type workflowEdgeDTO struct {
	ID          string `json:"id"`
	FromNodeKey string `json:"from_node_key"`
	ToNodeKey   string `json:"to_node_key"`
	Priority    int32  `json:"priority"`
}

type workflowTemplateDetailDTO struct {
	workflowTemplateDTO
	Nodes []workflowNodeDTO `json:"nodes"`
	Edges []workflowEdgeDTO `json:"edges"`
}

func workflowTemplateToDTO(t db.WorkflowTemplate) workflowTemplateDTO {
	return workflowTemplateDTO{
		ID:          uuidToString(t.ID),
		Key:         t.Key,
		Name:        t.Name,
		Description: t.Description,
		Version:     t.Version,
		Status:      t.Status,
		CreatedAt:   timestampToString(t.CreatedAt),
		UpdatedAt:   timestampToString(t.UpdatedAt),
	}
}

func workflowTemplateDetailToDTO(d *workflow.TemplateDetail) workflowTemplateDetailDTO {
	keyByID := map[string]string{}
	for _, n := range d.Nodes {
		keyByID[uuidToString(n.ID)] = n.NodeKey
	}
	nodes := make([]workflowNodeDTO, 0, len(d.Nodes))
	for _, n := range d.Nodes {
		nodes = append(nodes, workflowNodeDTO{
			ID:       uuidToString(n.ID),
			NodeKey:  n.NodeKey,
			Type:     n.Type,
			Name:     n.Name,
			Config:   n.Config,
			Position: n.Position,
		})
	}
	edges := make([]workflowEdgeDTO, 0, len(d.Edges))
	for _, e := range d.Edges {
		edges = append(edges, workflowEdgeDTO{
			ID:          uuidToString(e.ID),
			FromNodeKey: keyByID[uuidToString(e.FromNodeID)],
			ToNodeKey:   keyByID[uuidToString(e.ToNodeID)],
			Priority:    e.Priority,
		})
	}
	return workflowTemplateDetailDTO{
		workflowTemplateDTO: workflowTemplateToDTO(d.Template),
		Nodes:               nodes,
		Edges:               edges,
	}
}

// ---------------------------------------------------------------------------
// Request shapes
// ---------------------------------------------------------------------------

type workflowNodeInput struct {
	NodeKey  string          `json:"node_key"`
	Type     string          `json:"type"`
	Name     string          `json:"name"`
	Config   json.RawMessage `json:"config,omitempty"`
	Position json.RawMessage `json:"position,omitempty"`
}

type workflowEdgeInput struct {
	FromNodeKey string `json:"from_node_key"`
	ToNodeKey   string `json:"to_node_key"`
	Priority    int32  `json:"priority,omitempty"`
}

type createWorkflowTemplateRequest struct {
	Key         string              `json:"key"`
	Name        string              `json:"name"`
	Description string              `json:"description,omitempty"`
	Nodes       []workflowNodeInput `json:"nodes"`
	Edges       []workflowEdgeInput `json:"edges"`
}

type updateWorkflowTemplateRequest struct {
	Name        *string             `json:"name,omitempty"`
	Description *string             `json:"description,omitempty"`
	Nodes       []workflowNodeInput `json:"nodes,omitempty"`
	Edges       []workflowEdgeInput `json:"edges,omitempty"`
}

// toServiceInputs converts the transport graph, validating each node config
// eagerly so a malformed blob is a 400 here rather than a service error.
func toServiceInputs(nodes []workflowNodeInput, edges []workflowEdgeInput) ([]workflow.NodeInput, []workflow.EdgeInput, error) {
	nodeIn := make([]workflow.NodeInput, 0, len(nodes))
	for _, n := range nodes {
		if len(n.Config) > 0 {
			if _, err := workflow.ParseNodeConfig(n.Config); err != nil {
				return nil, nil, err
			}
		}
		nodeIn = append(nodeIn, workflow.NodeInput{
			NodeKey:  n.NodeKey,
			Type:     n.Type,
			Name:     n.Name,
			Config:   n.Config,
			Position: n.Position,
		})
	}
	edgeIn := make([]workflow.EdgeInput, 0, len(edges))
	for _, e := range edges {
		edgeIn = append(edgeIn, workflow.EdgeInput{
			FromNodeKey: e.FromNodeKey,
			ToNodeKey:   e.ToNodeKey,
			Priority:    e.Priority,
		})
	}
	return nodeIn, edgeIn, nil
}

// ---------------------------------------------------------------------------
// POST /api/workflow-templates
// ---------------------------------------------------------------------------

// CreateTemplate writes a draft template with its full graph. The creating
// member's user id is stamped as created_by.
func (h *WorkflowTemplateHandler) CreateTemplate(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := parseUUIDOrBadRequest(w, ctxWorkspaceID(r.Context()), "workspace_id")
	if !ok {
		return
	}
	member, ok := ctxMember(r.Context())
	if !ok {
		writeError(w, http.StatusForbidden, "workspace membership required")
		return
	}
	var req createWorkflowTemplateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Key == "" {
		writeError(w, http.StatusBadRequest, "key is required")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	nodes, edges, err := toServiceInputs(req.Nodes, req.Edges)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	detail, err := h.Templates.CreateTemplate(r.Context(), workflow.CreateTemplateParams{
		WorkspaceID: workspaceID,
		Key:         req.Key,
		Name:        req.Name,
		Description: req.Description,
		CreatedBy:   member.UserID,
		Nodes:       nodes,
		Edges:       edges,
	})
	if err != nil {
		// The service's failures here are graph validation or a constraint
		// hit (e.g. duplicate key/version) — all client-addressable, so 400
		// with the message; log for the unexpected-DB-failure case hiding in
		// the same bucket.
		slog.Warn("workflow template create failed", "error", err)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, workflowTemplateDetailToDTO(detail))
}

// ---------------------------------------------------------------------------
// GET /api/workflow-templates + GET /api/workflow-templates/{id}
// ---------------------------------------------------------------------------

// ListTemplates lists every template in the workspace (any status).
func (h *WorkflowTemplateHandler) ListTemplates(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := parseUUIDOrBadRequest(w, ctxWorkspaceID(r.Context()), "workspace_id")
	if !ok {
		return
	}
	templates, err := h.Templates.ListTemplates(r.Context(), workspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list templates")
		return
	}
	out := make([]workflowTemplateDTO, 0, len(templates))
	for _, t := range templates {
		out = append(out, workflowTemplateToDTO(t))
	}
	writeJSON(w, http.StatusOK, out)
}

// GetTemplate returns one template with its graph, scoped to the workspace.
func (h *WorkflowTemplateHandler) GetTemplate(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := parseUUIDOrBadRequest(w, ctxWorkspaceID(r.Context()), "workspace_id")
	if !ok {
		return
	}
	templateID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "id")
	if !ok {
		return
	}
	detail, err := h.Templates.GetTemplate(r.Context(), workspaceID, templateID)
	if err != nil {
		writeWorkflowTemplateError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, workflowTemplateDetailToDTO(detail))
}

// ---------------------------------------------------------------------------
// PUT /api/workflow-templates/{id} — draft-only edit
// ---------------------------------------------------------------------------

// UpdateTemplate edits a draft. nodes+edges together trigger a wholesale
// graph rewrite; supplying exactly one of them is a 400. A published or
// archived template is immutable — 409 (create a new version instead).
func (h *WorkflowTemplateHandler) UpdateTemplate(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := parseUUIDOrBadRequest(w, ctxWorkspaceID(r.Context()), "workspace_id")
	if !ok {
		return
	}
	templateID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "id")
	if !ok {
		return
	}
	var req updateWorkflowTemplateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	replaceGraph := req.Nodes != nil || req.Edges != nil
	if replaceGraph && (req.Nodes == nil || req.Edges == nil) {
		writeError(w, http.StatusBadRequest, "nodes and edges must be provided together to replace the graph")
		return
	}
	params := workflow.UpdateTemplateParams{
		WorkspaceID:  workspaceID,
		TemplateID:   templateID,
		Name:         req.Name,
		Description:  req.Description,
		ReplaceGraph: replaceGraph,
	}
	if replaceGraph {
		nodes, edges, err := toServiceInputs(req.Nodes, req.Edges)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		params.Nodes = nodes
		params.Edges = edges
	}
	// Pre-read for the 404/409 distinction: the service's draft-guarded
	// UPDATE collapses "no such template" and "not a draft" into one error.
	if _, err := h.Templates.GetTemplate(r.Context(), workspaceID, templateID); err != nil {
		writeWorkflowTemplateError(w, err)
		return
	}
	detail, err := h.Templates.UpdateTemplate(r.Context(), params)
	if err != nil {
		writeWorkflowTemplateError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, workflowTemplateDetailToDTO(detail))
}

// ---------------------------------------------------------------------------
// POST /api/workflow-templates/{id}/publish + /archive
// ---------------------------------------------------------------------------

// PublishTemplate freezes a draft (selector→UUID, evaluator separation,
// version lifecycle). Validation failures are structured: evaluator/shared
// agent → 422, unresolvable selector or malformed graph → 400.
func (h *WorkflowTemplateHandler) PublishTemplate(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := parseUUIDOrBadRequest(w, ctxWorkspaceID(r.Context()), "workspace_id")
	if !ok {
		return
	}
	templateID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "id")
	if !ok {
		return
	}
	detail, err := h.Templates.PublishTemplate(r.Context(), workspaceID, templateID)
	if err != nil {
		writeWorkflowTemplateError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, workflowTemplateDetailToDTO(detail))
}

// ArchiveTemplate retires a draft or published template.
func (h *WorkflowTemplateHandler) ArchiveTemplate(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := parseUUIDOrBadRequest(w, ctxWorkspaceID(r.Context()), "workspace_id")
	if !ok {
		return
	}
	templateID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "id")
	if !ok {
		return
	}
	tmpl, err := h.Templates.ArchiveTemplate(r.Context(), workspaceID, templateID)
	if err != nil {
		writeWorkflowTemplateError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, workflowTemplateToDTO(tmpl))
}

// ---------------------------------------------------------------------------
// POST /api/workflow-templates/seed — idempotent seed templates (R8)
// ---------------------------------------------------------------------------

// seedWorkflowTemplatesRequest carries optional agent-selector overrides; an
// empty body seeds with the default placeholder names.
type seedWorkflowTemplatesRequest struct {
	PlannerAgent     string `json:"planner_agent,omitempty"`
	ImplementerAgent string `json:"implementer_agent,omitempty"`
	GateAgent        string `json:"gate_agent,omitempty"`
	ReviewAgent      string `json:"review_agent,omitempty"`
}

type seedWorkflowTemplatesResponse struct {
	Templates []workflow.SeedResult `json:"templates"`
}

// SeedTemplates creates + publishes the standard/bugfix seed templates,
// skipping keys the workspace already has (idempotent — a repeat call is a
// 200 with seeded=false, never an error). Seeding is an explicit operator
// action (the `multica workflow seed` CLI calls here), never an implicit
// side effect of a read path.
func (h *WorkflowTemplateHandler) SeedTemplates(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := parseUUIDOrBadRequest(w, ctxWorkspaceID(r.Context()), "workspace_id")
	if !ok {
		return
	}
	member, ok := ctxMember(r.Context())
	if !ok {
		writeError(w, http.StatusForbidden, "workspace membership required")
		return
	}
	var req seedWorkflowTemplatesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	results, err := h.Templates.SeedTemplates(r.Context(), workflow.SeedTemplatesParams{
		WorkspaceID: workspaceID,
		CreatedBy:   member.UserID,
		Selectors: workflow.SeedAgentSelectors{
			Planner:     req.PlannerAgent,
			Implementer: req.ImplementerAgent,
			GateRunner:  req.GateAgent,
			Reviewer:    req.ReviewAgent,
		},
	})
	if err != nil {
		writeWorkflowTemplateError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, seedWorkflowTemplatesResponse{Templates: results})
}

// ---------------------------------------------------------------------------
// Error mapping
// ---------------------------------------------------------------------------

// writeWorkflowTemplateError maps template-service sentinel errors to HTTP.
func writeWorkflowTemplateError(w http.ResponseWriter, err error) {
	var sepErr *workflow.EvaluatorSeparationError
	switch {
	case errors.Is(err, workflow.ErrTemplateNotFound):
		writeError(w, http.StatusNotFound, "template not found")
	case errors.Is(err, workflow.ErrTemplateNotDraft):
		writeError(w, http.StatusConflict, "template is not a draft (published/archived templates are immutable)")
	case errors.Is(err, workflow.ErrTemplateConflict):
		writeError(w, http.StatusConflict, "template changed concurrently; re-read and retry")
	case errors.As(err, &sepErr):
		writeError(w, http.StatusUnprocessableEntity, sepErr.Error())
	case errors.Is(err, workflow.ErrAgentNotFound):
		writeError(w, http.StatusBadRequest, "agent selector does not resolve to a workspace agent")
	case errors.Is(err, workflow.ErrAgentAmbiguous):
		writeError(w, http.StatusBadRequest, "agent selector matches multiple agents")
	default:
		writeError(w, http.StatusBadRequest, err.Error())
	}
}
