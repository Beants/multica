package handler

// knowledge_health_test.go — P2-6 AC: stale list (time factor) + maturity update.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// TestListStaleCandidates pins the time factor: only pending candidates older
// than age_days surface; fresh + extracted are excluded.
func TestListStaleCandidates(t *testing.T) {
	ctx := context.Background()
	wsID := util.MustParseUUID(testWorkspaceID)

	// Old pending candidate (backdated).
	old, err := testHandler.Queries.CreateKnowledgeCandidate(ctx, db.CreateKnowledgeCandidateParams{
		WorkspaceID: wsID, SourceType: "manual", Content: "old tip",
	})
	if err != nil {
		t.Fatalf("create old: %v", err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE knowledge_candidate SET updated_at = now() - interval '40 days' WHERE id = $1`, old.ID); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	// Fresh pending candidate (not stale).
	if _, err := testHandler.Queries.CreateKnowledgeCandidate(ctx, db.CreateKnowledgeCandidateParams{
		WorkspaceID: wsID, SourceType: "manual", Content: "fresh tip",
	}); err != nil {
		t.Fatalf("create fresh: %v", err)
	}

	// stale?age_days=30 → only the old one.
	w := httptest.NewRecorder()
	testHandler.ListStaleCandidates(w, memberRequest(t, "GET", "/api/knowledge-candidates/stale?age_days=30", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("stale = %d; body=%s", w.Code, w.Body.String())
	}
	var list []knowledgeCandidateDTO
	json.Unmarshal(w.Body.Bytes(), &list)
	if len(list) == 0 {
		t.Fatalf("stale list empty, want the old candidate")
	}
	for _, c := range list {
		if c.Content == "fresh tip" {
			t.Fatalf("fresh candidate leaked into stale list")
		}
	}
}

// TestUpdateCandidateMaturity pins the maturity transition after review.
func TestUpdateCandidateMaturity(t *testing.T) {
	ctx := context.Background()
	wsID := util.MustParseUUID(testWorkspaceID)
	ws := util.UUIDToString(wsID)
	cand, err := testHandler.Queries.CreateKnowledgeCandidate(ctx, db.CreateKnowledgeCandidateParams{
		WorkspaceID: wsID, SourceType: "manual", Content: "review me " + ws,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	w := httptest.NewRecorder()
	testHandler.UpdateCandidateMaturity(w, withURLParam(memberRequest(t, "POST", "/api/knowledge-candidates/"+cand.ID.String()+"/maturity", map[string]any{
		"maturity": "verified",
	}), "id", cand.ID.String()))
	if w.Code != http.StatusOK {
		t.Fatalf("maturity = %d; body=%s", w.Code, w.Body.String())
	}
	var got knowledgeCandidateDTO
	json.Unmarshal(w.Body.Bytes(), &got)
	if got.Maturity != "verified" {
		t.Fatalf("maturity = %q, want verified", got.Maturity)
	}

	// Invalid maturity → 400.
	w2 := httptest.NewRecorder()
	testHandler.UpdateCandidateMaturity(w2, withURLParam(memberRequest(t, "POST", "/api/knowledge-candidates/"+cand.ID.String()+"/maturity", map[string]any{
		"maturity": "bogus",
	}), "id", cand.ID.String()))
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("bad maturity = %d, want 400", w2.Code)
	}
}
