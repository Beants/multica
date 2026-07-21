package handler

// event_store_test.go — P2-1 AC4/AC5: list API + dedup insert.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// TestListWorkflowEvents covers the read API + type filter: inserts two
// events, lists all (both present), then lists by type (no leak).
func TestListWorkflowEvents(t *testing.T) {
	ctx := context.Background()
	wsID := util.MustParseUUID(testWorkspaceID)
	// dedup_key must be unique per workspace (the real listener hashes type+
	// workspace+actor+payload); a static "list-alpha" would collide across
	// test runs and ON CONFLICT would update the stale row instead of
	// inserting into THIS run's workspace.
	ws := util.UUIDToString(wsID)

	if _, err := testHandler.Queries.InsertEvent(ctx, db.InsertEventParams{
		WorkspaceID: wsID, EventType: "test.alpha", Payload: []byte(`{"a":1}`), DedupKey: "list-alpha-" + ws,
	}); err != nil {
		t.Fatalf("insert alpha: %v", err)
	}
	if _, err := testHandler.Queries.InsertEvent(ctx, db.InsertEventParams{
		WorkspaceID: wsID, EventType: "test.beta", Payload: []byte(`{"b":2}`), DedupKey: "list-beta-" + ws,
	}); err != nil {
		t.Fatalf("insert beta: %v", err)
	}

	direct, derr := testHandler.Queries.ListEvents(ctx, db.ListEventsParams{WorkspaceID: wsID, Column2: "", Limit: 50})
	if derr != nil {
		t.Fatalf("direct ListEvents: %v", derr)
	}
	_ = direct // sanity: the query ran; the handler assertion below is the contract

	// List all → both types present.
	w := httptest.NewRecorder()
	testHandler.ListWorkflowEvents(w, memberRequest(t, "GET", "/api/workflow-events", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("list = %d; body=%s", w.Code, w.Body.String())
	}
	var list []eventStoreDTO
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	alphaFound, betaFound := false, false
	for _, ev := range list {
		if ev.EventType == "test.alpha" {
			alphaFound = true
		}
		if ev.EventType == "test.beta" {
			betaFound = true
		}
	}
	if !alphaFound || !betaFound {
		t.Fatalf("expected both test.alpha + test.beta in list; got %+v", list)
	}

	// List by type → only alpha.
	w2 := httptest.NewRecorder()
	testHandler.ListWorkflowEvents(w2, memberRequest(t, "GET", "/api/workflow-events?type=test.alpha", nil))
	json.Unmarshal(w2.Body.Bytes(), &list)
	for _, ev := range list {
		if ev.EventType != "test.alpha" {
			t.Fatalf("type filter leaked event %q", ev.EventType)
		}
	}
}

// TestEventStore_DedupInsert pins AC3: same dedup_key twice → 1 row (ON
// CONFLICT DO NOTHING). The listener retries/replays collapse.
func TestEventStore_DedupInsert(t *testing.T) {
	ctx := context.Background()
	wsID := util.MustParseUUID(testWorkspaceID)
	ws := util.UUIDToString(wsID)
	dk := "dedup-same-" + ws
	for i := 0; i < 2; i++ {
		if _, err := testHandler.Queries.InsertEvent(ctx, db.InsertEventParams{
			WorkspaceID: wsID, EventType: "test.dedup", Payload: []byte(`{}`), DedupKey: dk,
		}); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	var n int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM event_store WHERE dedup_key = $1`, dk).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("dedup: %d rows for same dedup_key, want 1 (ON CONFLICT)", n)
	}
}
