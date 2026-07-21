package handler

// metrics_test.go — P2-3 AC3: AggregateEventsByType distribution + ordering.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestListWorkflowMetrics(t *testing.T) {
	ctx := context.Background()
	wsID := util.MustParseUUID(testWorkspaceID)
	// AggregateEventsByType reads the WHOLE workspace; clear leftovers from
	// other tests so the counts are exact (CI runs the full suite).
	testPool.Exec(ctx, `DELETE FROM event_store WHERE workspace_id = $1`, wsID)
	ws := util.UUIDToString(wsID)

	// 3 alpha + 1 beta → alpha ranks first.
	for i := 0; i < 3; i++ {
		if _, err := testHandler.Queries.InsertEvent(ctx, db.InsertEventParams{
			WorkspaceID: wsID, EventType: "metric.alpha", Payload: []byte(`{}`), DedupKey: "metric-alpha-" + ws + "-" + string(rune('a'+i)),
		}); err != nil {
			t.Fatalf("insert alpha %d: %v", i, err)
		}
	}
	if _, err := testHandler.Queries.InsertEvent(ctx, db.InsertEventParams{
		WorkspaceID: wsID, EventType: "metric.beta", Payload: []byte(`{}`), DedupKey: "metric-beta-" + ws,
	}); err != nil {
		t.Fatalf("insert beta: %v", err)
	}

	w := httptest.NewRecorder()
	testHandler.ListWorkflowMetrics(w, memberRequest(t, "GET", "/api/workflow-metrics", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("metrics = %d; body=%s", w.Code, w.Body.String())
	}
	var rows []struct {
		EventType  string `json:"event_type"`
		EventCount int64  `json:"event_count"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode: %v", err)
	}
	byType := map[string]int64{}
	for _, r := range rows {
		byType[r.EventType] = r.EventCount
	}
	if byType["metric.alpha"] != 3 {
		t.Fatalf("alpha count = %d, want 3", byType["metric.alpha"])
	}
	if byType["metric.beta"] != 1 {
		t.Fatalf("beta count = %d, want 1", byType["metric.beta"])
	}
	// Ordering: alpha (3) before beta (1).
	if len(rows) >= 2 && rows[0].EventType != "metric.alpha" {
		t.Fatalf("ordering: first = %q, want metric.alpha (highest count)", rows[0].EventType)
	}
}
