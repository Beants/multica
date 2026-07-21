package handler

// outbound_webhook.go — P2-2 outbound webhook config CRUD. External systems
// register URLs here; the delivery path (cmd/server/outbound_delivery.go)
// POSTs event payloads to them with an HMAC-SHA256 signature.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type outboundWebhookDTO struct {
	ID          string   `json:"id"`
	WorkspaceID string   `json:"workspace_id"`
	Url         string   `json:"url"`
	EventTypes  []string `json:"event_types"`
	Active      bool     `json:"active"`
	CreatedAt   string   `json:"created_at"`
	// Secret is returned only on create (so the operator can provision it
	// once); omitted from list responses.
	Secret string `json:"secret,omitempty"`
}

type createOutboundWebhookRequest struct {
	Url        string   `json:"url"`
	EventTypes []string `json:"event_types"`
	Active     *bool    `json:"active,omitempty"`
}

// CreateOutboundWebhook POST /api/workflow-webhooks
func (h *Handler) CreateOutboundWebhook(w http.ResponseWriter, r *http.Request) {
	wsID, ok := parseUUIDOrBadRequest(w, ctxWorkspaceID(r.Context()), "workspace_id")
	if !ok {
		return
	}
	var req createOutboundWebhookRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Url == "" {
		writeError(w, http.StatusBadRequest, "url is required")
		return
	}
	secret := generateWebhookSecret()
	active := true
	if req.Active != nil {
		active = *req.Active
	}
	wh, err := h.Queries.CreateOutboundWebhook(r.Context(), db.CreateOutboundWebhookParams{
		WorkspaceID: wsID,
		Url:         req.Url,
		Secret:      secret,
		EventTypes:  req.EventTypes,
		Column5:     active,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create webhook")
		return
	}
	writeJSON(w, http.StatusCreated, outboundWebhookToDTO(wh, true))
}

// ListOutboundWebhooks GET /api/workflow-webhooks
func (h *Handler) ListOutboundWebhooks(w http.ResponseWriter, r *http.Request) {
	wsID, ok := parseUUIDOrBadRequest(w, ctxWorkspaceID(r.Context()), "workspace_id")
	if !ok {
		return
	}
	whs, err := h.Queries.ListOutboundWebhooks(r.Context(), wsID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list webhooks")
		return
	}
	out := make([]outboundWebhookDTO, 0, len(whs))
	for _, wh := range whs {
		out = append(out, outboundWebhookToDTO(wh, false)) // never leak secret on list
	}
	writeJSON(w, http.StatusOK, out)
}

// DeleteOutboundWebhook DELETE /api/workflow-webhooks/{id}
func (h *Handler) DeleteOutboundWebhook(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "id")
	if !ok {
		return
	}
	if err := h.Queries.DeleteOutboundWebhook(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete webhook")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func outboundWebhookToDTO(wh db.OutboundWebhook, includeSecret bool) outboundWebhookDTO {
	dto := outboundWebhookDTO{
		ID:          uuidToString(wh.ID),
		WorkspaceID: uuidToString(wh.WorkspaceID),
		Url:         wh.Url,
		EventTypes:  wh.EventTypes,
		Active:      wh.Active,
		CreatedAt:   timestampToString(wh.CreatedAt),
	}
	if includeSecret {
		dto.Secret = wh.Secret
	}
	return dto
}

// generateWebhookSecret returns a 64-hex-char signing secret (32 random bytes).
func generateWebhookSecret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}
