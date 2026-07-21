package handler

// insights_test.go — P2-10 AC3: insights share + outlier note.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestListWorkflowInsights(t *testing.T) {
	ctx := context.Background()
	wsID := util.MustParseUUID(testWorkspaceID)
	// Insights derive share from ALL workspace events; clear leftovers so
	// 8+2 = total 10 (exact 80%/20%).
	testPool.Exec(ctx, `DELETE FROM event_store WHERE workspace_id = $1`, wsID)
	ws := util.UUIDToString(wsID)

	// 8 dominant + 2 minor → dominant ~80% (≥40% → "dominates"), minor ~20% (≥20% → "significant").
	for i := 0; i < 8; i++ {
		if _, err := testHandler.Queries.InsertEvent(ctx, db.InsertEventParams{
			WorkspaceID: wsID, EventType: "insight.dominant", Payload: []byte(`{}`), DedupKey: "ins-dom-" + ws + "-" + string(rune('a'+i)),
		}); err != nil {
			t.Fatalf("insert dominant %d: %v", i, err)
		}
	}
	for i := 0; i < 2; i++ {
		if _, err := testHandler.Queries.InsertEvent(ctx, db.InsertEventParams{
			WorkspaceID: wsID, EventType: "insight.minor", Payload: []byte(`{}`), DedupKey: "ins-min-" + ws + "-" + string(rune('a'+i)),
		}); err != nil {
			t.Fatalf("insert minor %d: %v", i, err)
		}
	}

	w := httptest.NewRecorder()
	testHandler.ListWorkflowInsights(w, memberRequest(t, "GET", "/api/workflow-metrics/insights", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("insights = %d; body=%s", w.Code, w.Body.String())
	}
	var insights []metricInsightDTO
	if err := json.Unmarshal(w.Body.Bytes(), &insights); err != nil {
		t.Fatalf("decode: %v", err)
	}
	byType := map[string]metricInsightDTO{}
	for _, ins := range insights {
		byType[ins.EventType] = ins
	}
	if byType["insight.dominant"].Count != 8 {
		t.Fatalf("dominant count = %d, want 8", byType["insight.dominant"].Count)
	}
	if byType["insight.dominant"].SharePct != 80 {
		t.Fatalf("dominant share = %d, want 80", byType["insight.dominant"].SharePct)
	}
	if byType["insight.dominant"].Note == "" {
		t.Fatalf("dominant (80%%) should carry an outlier note")
	}
	if byType["insight.minor"].SharePct != 20 {
		t.Fatalf("minor share = %d, want 20", byType["insight.minor"].SharePct)
	}
}
