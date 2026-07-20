package handler

// workflow_rules_test.go — P1-4 Rules asset CRUD contract tests.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestWorkflowRules_CRUD exercises the full rule + binding lifecycle:
// create rule → list → create binding → list → delete binding → delete rule.
func TestWorkflowRules_CRUD(t *testing.T) {
	h := NewWorkflowRuleHandler(testHandler.Queries)

	// Create rule (status defaults to active, scope to workspace).
	w := httptest.NewRecorder()
	h.CreateRule(w, memberRequest(t, "POST", "/api/workflow-rules", map[string]any{
		"name": "pr-desc", "level": "soft", "content": "PR must include test coverage",
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("create rule = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	var rule workflowRuleDTO
	if err := json.Unmarshal(w.Body.Bytes(), &rule); err != nil {
		t.Fatalf("decode rule: %v", err)
	}
	if rule.Level != "soft" || rule.Status != "active" || rule.Scope != "workspace" {
		t.Fatalf("rule level/status/scope = %q/%q/%q, want soft/active/workspace", rule.Level, rule.Status, rule.Scope)
	}

	// List rules contains it.
	wl := httptest.NewRecorder()
	h.ListRules(wl, memberRequest(t, "GET", "/api/workflow-rules", nil))
	if wl.Code != http.StatusOK {
		t.Fatalf("list rules = %d", wl.Code)
	}
	var list []workflowRuleDTO
	if err := json.Unmarshal(wl.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list) == 0 {
		t.Fatalf("list empty after create")
	}

	// Create binding. target_id reuses the workspace UUID as a stand-in —
	// the handler only persists the UUID + target_type, it does not resolve
	// the target row (app-layer concern, by design — no FK).
	wb := httptest.NewRecorder()
	h.CreateBinding(wb, withURLParam(memberRequest(t, "POST", "/api/workflow-rules/"+rule.ID+"/bindings", map[string]any{"target_type": "agent", "target_id": rule.WorkspaceID}), "id", rule.ID))
	if wb.Code != http.StatusCreated {
		t.Fatalf("create binding = %d; body=%s", wb.Code, wb.Body.String())
	}
	var binding workflowRuleBindingDTO
	if err := json.Unmarshal(wb.Body.Bytes(), &binding); err != nil {
		t.Fatalf("decode binding: %v", err)
	}
	if binding.Enforcement != "context_inject" {
		t.Fatalf("enforcement = %q, want context_inject (default)", binding.Enforcement)
	}

	// List bindings.
	wlb := httptest.NewRecorder()
	h.ListBindings(wlb, withURLParam(memberRequest(t, "GET", "/api/workflow-rules/"+rule.ID+"/bindings", nil), "id", rule.ID))
	if wlb.Code != http.StatusOK {
		t.Fatalf("list bindings = %d", wlb.Code)
	}

	// Delete binding (nested URL params: id + bindingId).
	wdb := httptest.NewRecorder()
	h.DeleteBinding(wdb, withURLParam(withURLParam(memberRequest(t, "DELETE", "/api/workflow-rules/"+rule.ID+"/bindings/"+binding.ID, nil), "id", rule.ID), "bindingId", binding.ID))
	if wdb.Code != http.StatusNoContent {
		t.Fatalf("delete binding = %d, want 204", wdb.Code)
	}

	// Delete rule.
	wd := httptest.NewRecorder()
	h.DeleteRule(wd, withURLParam(memberRequest(t, "DELETE", "/api/workflow-rules/"+rule.ID, nil), "id", rule.ID))
	if wd.Code != http.StatusNoContent {
		t.Fatalf("delete rule = %d, want 204", wd.Code)
	}
}

// TestWorkflowRules_Validation rejects a bogus level.
func TestWorkflowRules_Validation(t *testing.T) {
	h := NewWorkflowRuleHandler(testHandler.Queries)
	w := httptest.NewRecorder()
	h.CreateRule(w, memberRequest(t, "POST", "/api/workflow-rules", map[string]any{"name": "x", "level": "bogus", "content": "y"}))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad level = %d, want 400", w.Code)
	}
}
