package handler

// agent_capability_test.go — P1-fe-3 capability management API contract.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestAgentCapabilities_CRUD covers the upsert → list → delete lifecycle.
// Upsert is keyed on (agent_id, capability_key); a second POST with the same
// key updates proficiency rather than creating a duplicate.
func TestAgentCapabilities_CRUD(t *testing.T) {
	agentID := createHandlerTestAgent(t, "Capability CRUD", nil)

	// Create (upsert) — proficiency 80.
	w := httptest.NewRecorder()
	testHandler.CreateAgentCapability(w, withURLParam(newRequest(http.MethodPost, "/api/agents/"+agentID+"/capabilities", map[string]any{
		"capability_key": "python",
		"proficiency":    80,
	}), "id", agentID))
	if w.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	var cap0 agentCapabilityDTO
	if err := json.Unmarshal(w.Body.Bytes(), &cap0); err != nil {
		t.Fatalf("decode cap: %v", err)
	}
	if cap0.CapabilityKey != "python" || cap0.Proficiency != 80 {
		t.Fatalf("cap = key=%q prof=%d, want python/80", cap0.CapabilityKey, cap0.Proficiency)
	}

	// Upsert same key → proficiency updates to 90, no duplicate.
	w2 := httptest.NewRecorder()
	testHandler.CreateAgentCapability(w2, withURLParam(newRequest(http.MethodPost, "/api/agents/"+agentID+"/capabilities", map[string]any{
		"capability_key": "python",
		"proficiency":    90,
	}), "id", agentID))
	if w2.Code != http.StatusCreated {
		t.Fatalf("upsert = %d, want 201", w2.Code)
	}

	// List → exactly one python row at proficiency 90.
	wl := httptest.NewRecorder()
	testHandler.GetAgentCapabilities(wl, withURLParam(newRequest(http.MethodGet, "/api/agents/"+agentID+"/capabilities", nil), "id", agentID))
	if wl.Code != http.StatusOK {
		t.Fatalf("list = %d", wl.Code)
	}
	var list []agentCapabilityDTO
	if err := json.Unmarshal(wl.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list) != 1 || list[0].CapabilityKey != "python" || list[0].Proficiency != 90 {
		t.Fatalf("list = %+v, want 1 row python/90", list)
	}

	// Delete by cap id.
	wd := httptest.NewRecorder()
	testHandler.DeleteAgentCapability(wd, withURLParams(newRequest(http.MethodDelete, "/api/agents/"+agentID+"/capabilities/"+list[0].ID, nil), "id", agentID, "capId", list[0].ID))
	if wd.Code != http.StatusNoContent {
		t.Fatalf("delete = %d, want 204", wd.Code)
	}

	// List empty after delete.
	wl2 := httptest.NewRecorder()
	testHandler.GetAgentCapabilities(wl2, withURLParam(newRequest(http.MethodGet, "/api/agents/"+agentID+"/capabilities", nil), "id", agentID))
	json.Unmarshal(wl2.Body.Bytes(), &list)
	if len(list) != 0 {
		t.Fatalf("after delete, list = %d rows, want 0", len(list))
	}
}

// TestAgentCapabilities_Validation rejects empty key + out-of-range proficiency.
func TestAgentCapabilities_Validation(t *testing.T) {
	agentID := createHandlerTestAgent(t, "Capability Validation", nil)
	w := httptest.NewRecorder()
	testHandler.CreateAgentCapability(w, withURLParam(newRequest(http.MethodPost, "/api/agents/"+agentID+"/capabilities", map[string]any{
		"capability_key": "",
		"proficiency":    150,
	}), "id", agentID))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad input = %d, want 400", w.Code)
	}
}
