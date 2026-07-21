package handler

// event_store.go — P2-1 read API over the global event log. Powers the
// dashboard feed (P2-4) + ad-hoc operator queries. Filters by workspace
// (required), optional event_type, and a since-cursor for newest-first paging.

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type eventStoreDTO struct {
	ID          string          `json:"id"`
	WorkspaceID string          `json:"workspace_id,omitempty"`
	EventType   string          `json:"event_type"`
	ActorType   string          `json:"actor_type,omitempty"`
	ActorID     string          `json:"actor_id,omitempty"`
	Payload     json.RawMessage `json:"payload,omitempty"`
	OccurredAt  string          `json:"occurred_at"`
}

// ListWorkflowEvents GET /api/workflow-events?type=&limit=
// workspace-scoped via ctxWorkspaceID (membership gate). limit defaults 50,
// capped at 200.
func (h *Handler) ListWorkflowEvents(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := parseUUIDOrBadRequest(w, ctxWorkspaceID(r.Context()), "workspace_id")
	if !ok {
		return
	}
	eventType := r.URL.Query().Get("type")
	limit := int32(50)
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 200 {
			limit = int32(n)
		}
	}
	events, err := h.Queries.ListEvents(r.Context(), db.ListEventsParams{
		WorkspaceID: workspaceID,
		Column2:     eventType,
		Limit:       limit,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list events")
		return
	}
	out := make([]eventStoreDTO, 0, len(events))
	for _, ev := range events {
		out = append(out, eventStoreToDTO(ev))
	}
	writeJSON(w, http.StatusOK, out)
}

func eventStoreToDTO(e db.EventStore) eventStoreDTO {
	return eventStoreDTO{
		ID:          util.UUIDToString(e.ID),
		WorkspaceID: util.UUIDToString(e.WorkspaceID),
		EventType:   e.EventType,
		ActorType:   e.ActorType.String,
		ActorID:     util.UUIDToString(e.ActorID),
		Payload:     json.RawMessage(e.Payload),
		OccurredAt:  timestampToString(e.OccurredAt),
	}
}
