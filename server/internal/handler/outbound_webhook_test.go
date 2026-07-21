package handler

// outbound_webhook_test.go — P2-2 AC2: outbound webhook config CRUD.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestOutboundWebhooks_CRUD covers create (returns secret once) → list (no
// secret leak) → delete.
func TestOutboundWebhooks_CRUD(t *testing.T) {
	// Create — secret is generated + returned.
	w := httptest.NewRecorder()
	testHandler.CreateOutboundWebhook(w, memberRequest(t, "POST", "/api/workflow-webhooks", map[string]any{
		"url":         "https://example.com/hook",
		"event_types": []string{"test.event", "run.paused"},
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	var wh outboundWebhookDTO
	if err := json.Unmarshal(w.Body.Bytes(), &wh); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if wh.Url != "https://example.com/hook" {
		t.Fatalf("url = %q", wh.Url)
	}
	if len(wh.EventTypes) != 2 {
		t.Fatalf("event_types = %v, want 2", wh.EventTypes)
	}
	if len(wh.Secret) < 32 {
		t.Fatalf("secret too short (%d), want ≥32 hex chars", len(wh.Secret))
	}
	if !wh.Active {
		t.Fatalf("active = false, want true (default)")
	}

	// List — secret is NOT included.
	wl := httptest.NewRecorder()
	testHandler.ListOutboundWebhooks(wl, memberRequest(t, "GET", "/api/workflow-webhooks", nil))
	if wl.Code != http.StatusOK {
		t.Fatalf("list = %d", wl.Code)
	}
	var list []outboundWebhookDTO
	if err := json.Unmarshal(wl.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("list = %d rows, want 1", len(list))
	}
	if list[0].Secret != "" {
		t.Fatalf("list leaked secret: %q", list[0].Secret)
	}

	// Delete.
	wd := httptest.NewRecorder()
	testHandler.DeleteOutboundWebhook(wd, withURLParam(memberRequest(t, "DELETE", "/api/workflow-webhooks/"+wh.ID, nil), "id", wh.ID))
	if wd.Code != http.StatusNoContent {
		t.Fatalf("delete = %d, want 204", wd.Code)
	}

	// List empty after delete.
	wl2 := httptest.NewRecorder()
	testHandler.ListOutboundWebhooks(wl2, memberRequest(t, "GET", "/api/workflow-webhooks", nil))
	json.Unmarshal(wl2.Body.Bytes(), &list)
	if len(list) != 0 {
		t.Fatalf("after delete, list = %d, want 0", len(list))
	}
}

// TestOutboundWebhooks_Validation rejects a missing url.
func TestOutboundWebhooks_Validation(t *testing.T) {
	w := httptest.NewRecorder()
	testHandler.CreateOutboundWebhook(w, memberRequest(t, "POST", "/api/workflow-webhooks", map[string]any{
		"event_types": []string{"x"},
	}))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing url = %d, want 400", w.Code)
	}
}
