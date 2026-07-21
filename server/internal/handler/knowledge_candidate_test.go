package handler

// knowledge_candidate_test.go — P2-5 AC5: candidate CRUD + extract→Rule.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestKnowledgeCandidates_CRUDAndExtract(t *testing.T) {
	// Create candidate (defaults: status=pending, maturity=draft).
	w := httptest.NewRecorder()
	testHandler.CreateKnowledgeCandidate(w, memberRequest(t, "POST", "/api/knowledge-candidates", map[string]any{
		"content":       "always pin go.mod before opening a PR",
		"suggested_key": "go-mod-pin",
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("create = %d; body=%s", w.Code, w.Body.String())
	}
	var cand knowledgeCandidateDTO
	if err := json.Unmarshal(w.Body.Bytes(), &cand); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if cand.Status != "pending" || cand.Maturity != "draft" {
		t.Fatalf("status/maturity = %q/%q, want pending/draft", cand.Status, cand.Maturity)
	}
	if cand.Content != "always pin go.mod before opening a PR" {
		t.Fatalf("content = %q", cand.Content)
	}

	// List → candidate present.
	wl := httptest.NewRecorder()
	testHandler.ListKnowledgeCandidates(wl, memberRequest(t, "GET", "/api/knowledge-candidates", nil))
	if wl.Code != http.StatusOK {
		t.Fatalf("list = %d", wl.Code)
	}
	var list []knowledgeCandidateDTO
	json.Unmarshal(wl.Body.Bytes(), &list)
	if len(list) == 0 {
		t.Fatalf("list empty after create")
	}

	// Extract → soft Rule created + candidate marked extracted.
	we := httptest.NewRecorder()
	testHandler.ExtractKnowledgeCandidateToRule(we, withURLParam(memberRequest(t, "POST", "/api/knowledge-candidates/"+cand.ID+"/extract", nil), "id", cand.ID))
	if we.Code != http.StatusCreated {
		t.Fatalf("extract = %d; body=%s", we.Code, we.Body.String())
	}
	var resp struct {
		Candidate knowledgeCandidateDTO `json:"candidate"`
		Rule      workflowRuleDTO       `json:"rule"`
	}
	if err := json.Unmarshal(we.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode extract: %v", err)
	}
	if resp.Candidate.Status != "extracted" {
		t.Fatalf("candidate status after extract = %q, want extracted", resp.Candidate.Status)
	}
	if resp.Rule.Level != "soft" || resp.Rule.Name != "go-mod-pin" {
		t.Fatalf("rule = level=%q name=%q, want soft/go-mod-pin", resp.Rule.Level, resp.Rule.Name)
	}

	// Re-extract → 409 (guarded: not pending anymore).
	we2 := httptest.NewRecorder()
	testHandler.ExtractKnowledgeCandidateToRule(we2, withURLParam(memberRequest(t, "POST", "/api/knowledge-candidates/"+cand.ID+"/extract", nil), "id", cand.ID))
	if we2.Code != http.StatusConflict {
		t.Fatalf("re-extract = %d, want 409", we2.Code)
	}

	// Delete candidate (rule stays — it's a separate promoted asset).
	wd := httptest.NewRecorder()
	testHandler.DeleteKnowledgeCandidate(wd, withURLParam(memberRequest(t, "DELETE", "/api/knowledge-candidates/"+cand.ID, nil), "id", cand.ID))
	if wd.Code != http.StatusNoContent {
		t.Fatalf("delete = %d, want 204", wd.Code)
	}
}
