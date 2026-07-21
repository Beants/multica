package handler

// metrics.go — P2-3 aggregated metrics read API. MVP: scene-layer event_type
// distribution (count per event_type) straight off event_store. The other
// three layers (agent / run / node) are follow-ups once the dashboard (P2-4)
// pins which buckets it renders.

import (
	"net/http"
)

// ListWorkflowMetrics GET /api/workflow-metrics
// Returns the workspace's event_type distribution (scene layer) for the
// dashboard. workspace-scoped via ctxWorkspaceID.
func (h *Handler) ListWorkflowMetrics(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := parseUUIDOrBadRequest(w, ctxWorkspaceID(r.Context()), "workspace_id")
	if !ok {
		return
	}
	rows, err := h.Queries.AggregateEventsByType(r.Context(), workspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to aggregate metrics")
		return
	}
	// rows is []AggregateEventsByTypeRow{event_type, event_count} — already
	// JSON-shaped; marshal straight through (empty slice -> []).
	writeJSON(w, http.StatusOK, rows)
}
