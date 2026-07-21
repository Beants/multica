package handler

// knowledge_candidate.go — P2-5 knowledge sediment pool. Operators mark
// candidates during runs; extract promotes a pending candidate into a soft
// Rule (P1-4) so the knowledge starts guiding agents.

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type knowledgeCandidateDTO struct {
	ID           string `json:"id"`
	WorkspaceID  string `json:"workspace_id"`
	SourceType   string `json:"source_type"`
	SourceID     string `json:"source_id,omitempty"`
	Content      string `json:"content"`
	SuggestedKey string `json:"suggested_key,omitempty"`
	Status       string `json:"status"`
	Maturity     string `json:"maturity"`
	CreatedAt    string `json:"created_at"`
}

type createCandidateRequest struct {
	SourceType   string `json:"source_type"`
	SourceID     string `json:"source_id"`
	Content      string `json:"content"`
	SuggestedKey string `json:"suggested_key"`
}

// CreateKnowledgeCandidate POST /api/knowledge-candidates
func (h *Handler) CreateKnowledgeCandidate(w http.ResponseWriter, r *http.Request) {
	wsID, ok := parseUUIDOrBadRequest(w, ctxWorkspaceID(r.Context()), "workspace_id")
	if !ok {
		return
	}
	var req createCandidateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Content == "" {
		writeError(w, http.StatusBadRequest, "content is required")
		return
	}
	sourceType := req.SourceType
	if sourceType == "" {
		sourceType = "manual"
	}
	var sourceID pgtype.UUID
	if req.SourceID != "" {
		sourceID, _ = util.ParseUUID(req.SourceID)
	}
	var suggestedKey pgtype.Text
	if req.SuggestedKey != "" {
		suggestedKey = pgtype.Text{String: req.SuggestedKey, Valid: true}
	}
	c, err := h.Queries.CreateKnowledgeCandidate(r.Context(), db.CreateKnowledgeCandidateParams{
		WorkspaceID:  wsID,
		SourceType:   sourceType,
		SourceID:     sourceID,
		Content:      req.Content,
		SuggestedKey: suggestedKey,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create candidate")
		return
	}
	writeJSON(w, http.StatusCreated, candidateToDTO(c))
}

// ListKnowledgeCandidates GET /api/knowledge-candidates?status=
func (h *Handler) ListKnowledgeCandidates(w http.ResponseWriter, r *http.Request) {
	wsID, ok := parseUUIDOrBadRequest(w, ctxWorkspaceID(r.Context()), "workspace_id")
	if !ok {
		return
	}
	status := r.URL.Query().Get("status")
	cands, err := h.Queries.ListKnowledgeCandidates(r.Context(), db.ListKnowledgeCandidatesParams{
		WorkspaceID: wsID,
		Column2:     status,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list candidates")
		return
	}
	out := make([]knowledgeCandidateDTO, 0, len(cands))
	for _, c := range cands {
		out = append(out, candidateToDTO(c))
	}
	writeJSON(w, http.StatusOK, out)
}

// ExtractKnowledgeCandidateToRule POST /api/knowledge-candidates/{id}/extract
// Promote a pending candidate into a soft Rule (level=soft, scope=workspace),
// marking it extracted (guarded against double-promotion).
func (h *Handler) ExtractKnowledgeCandidateToRule(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "id")
	if !ok {
		return
	}
	cand, err := h.Queries.UpdateKnowledgeCandidateStatus(r.Context(), db.UpdateKnowledgeCandidateStatusParams{
		ID:      id,
		Status:  "extracted",
		Status_2: "pending",
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusConflict, "candidate not pending (already extracted/rejected)")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to update candidate")
		return
	}
	name := cand.SuggestedKey.String
	if name == "" {
		name = "extracted-" + util.UUIDToString(cand.ID)[:8]
	}
	rule, err := h.Queries.CreateWorkflowRule(r.Context(), db.CreateWorkflowRuleParams{
		WorkspaceID: cand.WorkspaceID,
		Name:        name,
		Level:       "soft",
		Scope:       "workspace",
		Content:     cand.Content,
		Config:      []byte("{}"),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "candidate marked extracted but rule creation failed")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"candidate": candidateToDTO(cand),
		"rule":      ruleToDTO(rule),
	})
}

// DeleteKnowledgeCandidate DELETE /api/knowledge-candidates/{id}
func (h *Handler) DeleteKnowledgeCandidate(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "id")
	if !ok {
		return
	}
	if err := h.Queries.DeleteKnowledgeCandidate(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete candidate")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListStaleCandidates GET /api/knowledge-candidates/stale?age_days=30
// P2-6 health: pending candidates older than age_days need review (time factor;
// code-change correlation is the second factor, follow-up).
func (h *Handler) ListStaleCandidates(w http.ResponseWriter, r *http.Request) {
	wsID, ok := parseUUIDOrBadRequest(w, ctxWorkspaceID(r.Context()), "workspace_id")
	if !ok {
		return
	}
	ageDays := 30
	if d := r.URL.Query().Get("age_days"); d != "" {
		if n, err := strconv.Atoi(d); err == nil && n > 0 {
			ageDays = n
		}
	}
	cands, err := h.Queries.ListStaleCandidates(r.Context(), db.ListStaleCandidatesParams{
		WorkspaceID: wsID,
		Column2:     int32(ageDays * 86400),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list stale candidates")
		return
	}
	out := make([]knowledgeCandidateDTO, 0, len(cands))
	for _, c := range cands {
		out = append(out, candidateToDTO(c))
	}
	writeJSON(w, http.StatusOK, out)
}

// UpdateCandidateMaturity POST /api/knowledge-candidates/{id}/maturity
// P2-6: mark a candidate's maturity after review.
func (h *Handler) UpdateCandidateMaturity(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "id")
	if !ok {
		return
	}
	var req struct {
		Maturity string `json:"maturity"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	valid := map[string]bool{"draft": true, "verified": true, "proven": true, "stale": true, "conflict": true}
	if !valid[req.Maturity] {
		writeError(w, http.StatusBadRequest, "maturity must be draft/verified/proven/stale/conflict")
		return
	}
	cand, err := h.Queries.UpdateCandidateMaturity(r.Context(), db.UpdateCandidateMaturityParams{
		ID:       id,
		Maturity: req.Maturity,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update maturity")
		return
	}
	writeJSON(w, http.StatusOK, candidateToDTO(cand))
}

func candidateToDTO(c db.KnowledgeCandidate) knowledgeCandidateDTO {
	return knowledgeCandidateDTO{
		ID:           uuidToString(c.ID),
		WorkspaceID:  uuidToString(c.WorkspaceID),
		SourceType:   c.SourceType,
		SourceID:     uuidToString(c.SourceID),
		Content:      c.Content,
		SuggestedKey: c.SuggestedKey.String,
		Status:       c.Status,
		Maturity:     c.Maturity,
		CreatedAt:    timestampToString(c.CreatedAt),
	}
}
