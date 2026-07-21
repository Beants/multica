package handler

// agent_capability.go — P1-fe-3 capability management API. Exposes the
// agent_capability table (P1-7) so the UI can label agent proficiency; the
// dispatch matcher (MatchAgentByCapability) consumes these rows at runtime.
//
// Auth reuses loadAgentForUser (workspace member gate, same as GetAgent).
// These are agent sub-resources, not workflow routes, so they sit outside the
// workflow_engine flag gate — capabilities can be labeled even when the
// engine is off (the matcher is simply never queried then).

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type agentCapabilityDTO struct {
	ID            string          `json:"id"`
	AgentID       string          `json:"agent_id"`
	CapabilityKey string          `json:"capability_key"`
	Proficiency   int16           `json:"proficiency"`
	Evidence      json.RawMessage `json:"evidence,omitempty"`
	UpdatedAt     string          `json:"updated_at"`
	CreatedAt     string          `json:"created_at"`
}

type createAgentCapabilityRequest struct {
	CapabilityKey string `json:"capability_key"`
	Proficiency   int16  `json:"proficiency"`
}

// GetAgentCapabilities GET /api/agents/{id}/capabilities
func (h *Handler) GetAgentCapabilities(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.loadAgentForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	caps, err := h.Queries.ListAgentCapabilities(r.Context(), agent.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list capabilities")
		return
	}
	out := make([]agentCapabilityDTO, 0, len(caps))
	for _, c := range caps {
		out = append(out, capabilityToDTO(c))
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateAgentCapability POST /api/agents/{id}/capabilities (upsert by
// capability_key — the (agent_id, capability_key) unique index backs it).
func (h *Handler) CreateAgentCapability(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.loadAgentForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var req createAgentCapabilityRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.CapabilityKey == "" {
		writeError(w, http.StatusBadRequest, "capability_key is required")
		return
	}
	if req.Proficiency < 0 || req.Proficiency > 100 {
		writeError(w, http.StatusBadRequest, "proficiency must be between 0 and 100")
		return
	}
	cap, err := h.Queries.UpsertAgentCapability(r.Context(), db.UpsertAgentCapabilityParams{
		AgentID:       agent.ID,
		CapabilityKey: req.CapabilityKey,
		Proficiency:   req.Proficiency,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save capability")
		return
	}
	writeJSON(w, http.StatusCreated, capabilityToDTO(cap))
}

// DeleteAgentCapability DELETE /api/agents/{id}/capabilities/{capId}
func (h *Handler) DeleteAgentCapability(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.loadAgentForUser(w, r, chi.URLParam(r, "id")); !ok {
		return
	}
	capID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "capId"), "capId")
	if !ok {
		return
	}
	if err := h.Queries.DeleteAgentCapability(r.Context(), capID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete capability")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func capabilityToDTO(c db.AgentCapability) agentCapabilityDTO {
	return agentCapabilityDTO{
		ID:            uuidToString(c.ID),
		AgentID:       uuidToString(c.AgentID),
		CapabilityKey: c.CapabilityKey,
		Proficiency:   c.Proficiency,
		Evidence:      json.RawMessage(c.Evidence),
		UpdatedAt:     timestampToString(c.UpdatedAt),
		CreatedAt:     timestampToString(c.CreatedAt),
	}
}
