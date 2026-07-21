package handler

// insights.go — P2-10 metrics-analysis agent prototype. Consumes P2-3's
// AggregateEventsByType and surfaces structured insights (top event types +
// their share) — the "which agents/scenarios deserve investment" question
// answered from data, not vibes. LLM-backed natural-language analysis is a
// follow-up; this is the deterministic structuring layer.

import (
	"net/http"
)

type metricInsightDTO struct {
	EventType string `json:"event_type"`
	Count     int64  `json:"count"`
	SharePct  int    `json:"share_pct"`
	Note      string `json:"note"`
}

// ListWorkflowInsights GET /api/workflow-metrics/insights
// Returns the top event types with their share of total events + a one-line
// note flagging outliers (≥40% share = "dominates the feed, investigate").
func (h *Handler) ListWorkflowInsights(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := parseUUIDOrBadRequest(w, ctxWorkspaceID(r.Context()), "workspace_id")
	if !ok {
		return
	}
	rows, err := h.Queries.AggregateEventsByType(r.Context(), workspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to aggregate metrics")
		return
	}
	var total int64
	for _, row := range rows {
		total += row.EventCount
	}
	insights := make([]metricInsightDTO, 0, len(rows))
	for _, row := range rows {
		share := 0
		if total > 0 {
			share = int((row.EventCount * 100) / total)
		}
		note := ""
		if share >= 40 {
			note = "dominates the event feed — investigate whether this is signal or noise"
		} else if share >= 20 {
			note = "significant share — worth a closer look"
		}
		insights = append(insights, metricInsightDTO{
			EventType: row.EventType,
			Count:     row.EventCount,
			SharePct:  share,
			Note:      note,
		})
	}
	writeJSON(w, http.StatusOK, insights)
}
