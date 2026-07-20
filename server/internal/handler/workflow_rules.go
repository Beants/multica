package handler

// workflow_rules.go — P1-4 Rules asset CRUD API (design.md §2 支柱 5). Rules
// are team constraints (hard/soft/safety) bound to node/template/agent/project
// via rule_binding. P1-4 MVP delivers the data layer + CRUD + soft
// context_inject handoff注入 (engine-side, rework.go buildHandoffNote); hard
// gate-check execution (gate_type=rules) lands in P1-4b.

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// WorkflowRuleHandler serves /api/workflow-rules*. Operator surface: every
// route sits behind RequireHumanActor (rules are a team-governance asset, not
// an agent self-service).
type WorkflowRuleHandler struct {
	Queries *db.Queries
}

func NewWorkflowRuleHandler(q *db.Queries) *WorkflowRuleHandler {
	return &WorkflowRuleHandler{Queries: q}
}

type workflowRuleDTO struct {
	ID          string          `json:"id"`
	WorkspaceID string          `json:"workspace_id"`
	Name        string          `json:"name"`
	Level       string          `json:"level"`
	Scope       string          `json:"scope"`
	Content     string          `json:"content"`
	Config      json.RawMessage `json:"config,omitempty"`
	Status      string          `json:"status"`
	Version     int32           `json:"version"`
	CreatedAt   string          `json:"created_at"`
	UpdatedAt   string          `json:"updated_at"`
}

type workflowRuleBindingDTO struct {
	ID          string `json:"id"`
	RuleID      string `json:"rule_id"`
	TargetType  string `json:"target_type"`
	TargetID    string `json:"target_id"`
	Enforcement string `json:"enforcement"`
	CreatedAt   string `json:"created_at"`
}

type createRuleRequest struct {
	Name    string          `json:"name"`
	Level   string          `json:"level"`
	Scope   string          `json:"scope"`
	Content string          `json:"content"`
	Config  json.RawMessage `json:"config,omitempty"`
	Status  string          `json:"status,omitempty"`
}

type createBindingRequest struct {
	TargetType  string `json:"target_type"`
	TargetID    string `json:"target_id"`
	Enforcement string `json:"enforcement,omitempty"`
}

// CreateRule POST /api/workflow-rules
func (h *WorkflowRuleHandler) CreateRule(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := parseUUIDOrBadRequest(w, ctxWorkspaceID(r.Context()), "workspace_id")
	if !ok {
		return
	}
	var req createRuleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" || req.Content == "" {
		writeError(w, http.StatusBadRequest, "name and content are required")
		return
	}
	if !validRuleLevel(req.Level) {
		writeError(w, http.StatusBadRequest, "level must be one of hard/soft/safety")
		return
	}
	scope := req.Scope
	if scope == "" {
		scope = "workspace"
	}
	if len(req.Config) == 0 {
		req.Config = json.RawMessage("{}")
	}
	rule, err := h.Queries.CreateWorkflowRule(r.Context(), db.CreateWorkflowRuleParams{
		WorkspaceID: workspaceID,
		Name:        req.Name,
		Level:       req.Level,
		Scope:       scope,
		Content:     req.Content,
		Config:      req.Config,
		Column7:     req.Status, // COALESCE -> 'active' when empty
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create rule")
		return
	}
	writeJSON(w, http.StatusCreated, ruleToDTO(rule))
}

// ListRules GET /api/workflow-rules
func (h *WorkflowRuleHandler) ListRules(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := parseUUIDOrBadRequest(w, ctxWorkspaceID(r.Context()), "workspace_id")
	if !ok {
		return
	}
	rules, err := h.Queries.ListWorkflowRules(r.Context(), workspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list rules")
		return
	}
	out := make([]workflowRuleDTO, 0, len(rules))
	for _, rule := range rules {
		out = append(out, ruleToDTO(rule))
	}
	writeJSON(w, http.StatusOK, out)
}

// DeleteRule DELETE /api/workflow-rules/{id}
func (h *WorkflowRuleHandler) DeleteRule(w http.ResponseWriter, r *http.Request) {
	ruleID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "id")
	if !ok {
		return
	}
	if err := h.Queries.DeleteWorkflowRule(r.Context(), ruleID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete rule")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// CreateBinding POST /api/workflow-rules/{id}/bindings
func (h *WorkflowRuleHandler) CreateBinding(w http.ResponseWriter, r *http.Request) {
	ruleID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "id")
	if !ok {
		return
	}
	var req createBindingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !validRuleTargetType(req.TargetType) {
		writeError(w, http.StatusBadRequest, "target_type must be one of node/template/agent/project")
		return
	}
	targetID, ok := parseUUIDOrBadRequest(w, req.TargetID, "target_id")
	if !ok {
		return
	}
	b, err := h.Queries.CreateWorkflowRuleBinding(r.Context(), db.CreateWorkflowRuleBindingParams{
		RuleID:     ruleID,
		TargetType: req.TargetType,
		TargetID:   targetID,
		Column4:    req.Enforcement, // COALESCE -> 'context_inject' when empty
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create binding")
		return
	}
	writeJSON(w, http.StatusCreated, bindingToDTO(b))
}

// ListBindings GET /api/workflow-rules/{id}/bindings
func (h *WorkflowRuleHandler) ListBindings(w http.ResponseWriter, r *http.Request) {
	ruleID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "id")
	if !ok {
		return
	}
	bindings, err := h.Queries.ListWorkflowRuleBindings(r.Context(), ruleID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list bindings")
		return
	}
	out := make([]workflowRuleBindingDTO, 0, len(bindings))
	for _, b := range bindings {
		out = append(out, bindingToDTO(b))
	}
	writeJSON(w, http.StatusOK, out)
}

// DeleteBinding DELETE /api/workflow-rules/{id}/bindings/{bindingId}
func (h *WorkflowRuleHandler) DeleteBinding(w http.ResponseWriter, r *http.Request) {
	bindingID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "bindingId"), "bindingId")
	if !ok {
		return
	}
	if err := h.Queries.DeleteWorkflowRuleBinding(r.Context(), bindingID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete binding")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func ruleToDTO(r db.WorkflowRule) workflowRuleDTO {
	return workflowRuleDTO{
		ID:          uuidToString(r.ID),
		WorkspaceID: uuidToString(r.WorkspaceID),
		Name:        r.Name,
		Level:       r.Level,
		Scope:       r.Scope,
		Content:     r.Content,
		Config:      json.RawMessage(r.Config),
		Status:      r.Status,
		Version:     r.Version,
		CreatedAt:   timestampToString(r.CreatedAt),
		UpdatedAt:   timestampToString(r.UpdatedAt),
	}
}

func bindingToDTO(b db.WorkflowRuleBinding) workflowRuleBindingDTO {
	return workflowRuleBindingDTO{
		ID:          uuidToString(b.ID),
		RuleID:      uuidToString(b.RuleID),
		TargetType:  b.TargetType,
		TargetID:    uuidToString(b.TargetID),
		Enforcement: b.Enforcement,
		CreatedAt:   timestampToString(b.CreatedAt),
	}
}

func validRuleLevel(l string) bool {
	return l == "hard" || l == "soft" || l == "safety"
}

func validRuleTargetType(t string) bool {
	return t == "node" || t == "template" || t == "agent" || t == "project"
}
