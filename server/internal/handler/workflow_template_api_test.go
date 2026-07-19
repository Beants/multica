package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/workflow"
)

// workflow_template_api_test.go — API contract tests for the human-facing
// template management endpoints. The publish lifecycle semantics themselves
// (snapshot freeze, selector→UUID, evaluator separation, version archive) are
// covered by internal/workflow/template_test.go; here we assert the
// transport: auth context, workspace scoping, and sentinel → HTTP mapping.

// templateAPIFixture builds the handler under test and tracks created
// template ids for cleanup.
type templateAPIFixture struct {
	th          *WorkflowTemplateHandler
	templateIDs []string
}

func setupTemplateAPI(t *testing.T) *templateAPIFixture {
	t.Helper()
	f := &templateAPIFixture{
		th: NewWorkflowTemplateHandler(testHandler.Queries, workflow.NewTemplateService(testHandler.Queries, testPool)),
	}
	t.Cleanup(func() {
		for _, id := range f.templateIDs {
			testPool.Exec(context.Background(), `DELETE FROM workflow_template WHERE id = $1`, id)
		}
	})
	return f
}

// createViaAPI posts a create and returns the decoded detail.
func (f *templateAPIFixture) createViaAPI(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	f.th.CreateTemplate(w, memberRequest(t, "POST", "/api/workflow-templates", body))
	if w.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if id, _ := resp["id"].(string); id != "" {
		f.templateIDs = append(f.templateIDs, id)
	}
	return resp
}

// simpleGraph is a valid two-node chain: work(agent, executor) → end. The
// selector uses the workspace's seeded agent ("Handler Test Agent").
func simpleGraph() map[string]any {
	return map[string]any{
		"nodes": []map[string]any{
			{"node_key": "work", "type": "agent", "name": "Work",
				"config": map[string]any{"role": "executor", "agent_selector": "Handler Test Agent"}},
			{"node_key": "end", "type": "end", "name": "End", "config": map[string]any{}},
		},
		"edges": []map[string]any{
			{"from_node_key": "work", "to_node_key": "end"},
		},
	}
}

// TestWorkflowTemplateAPILifecycle drives the full management flow:
// create → list → get → draft edit → publish → (immutability guards) → archive.
func TestWorkflowTemplateAPILifecycle(t *testing.T) {
	f := setupTemplateAPI(t)
	key := fmt.Sprintf("api-lifecycle-%d", time.Now().UnixNano())

	body := simpleGraph()
	body["key"] = key
	body["name"] = "Lifecycle"
	created := f.createViaAPI(t, body)
	id, _ := created["id"].(string)
	if created["status"] != "draft" {
		t.Fatalf("status = %v, want draft", created["status"])
	}
	if nodes, _ := created["nodes"].([]any); len(nodes) != 2 {
		t.Fatalf("nodes = %d, want 2", len(nodes))
	}
	// Edges are exposed by node_key, not row UUIDs.
	edges, _ := created["edges"].([]any)
	if len(edges) != 1 {
		t.Fatalf("edges = %d, want 1", len(edges))
	}
	if edges[0].(map[string]any)["from_node_key"] != "work" || edges[0].(map[string]any)["to_node_key"] != "end" {
		t.Fatalf("edge keys wrong: %v", edges[0])
	}

	// List contains the template.
	wl := httptest.NewRecorder()
	f.th.ListTemplates(wl, memberRequest(t, "GET", "/api/workflow-templates", nil))
	if wl.Code != http.StatusOK {
		t.Fatalf("list = %d, want 200", wl.Code)
	}
	var list []map[string]any
	if err := json.Unmarshal(wl.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	found := false
	for _, item := range list {
		if item["id"] == id {
			found = true
		}
	}
	if !found {
		t.Fatalf("list missing created template %s", id)
	}

	// Get detail.
	wg := httptest.NewRecorder()
	f.th.GetTemplate(wg, withURLParam(memberRequest(t, "GET", "/api/workflow-templates/"+id, nil), "id", id))
	if wg.Code != http.StatusOK {
		t.Fatalf("get = %d, want 200; body=%s", wg.Code, wg.Body.String())
	}

	// Draft edit (name only).
	newName := "Lifecycle v2"
	wu := httptest.NewRecorder()
	f.th.UpdateTemplate(wu, withURLParam(memberRequest(t, "PUT", "/api/workflow-templates/"+id, map[string]any{
		"name": newName,
	}), "id", id))
	if wu.Code != http.StatusOK {
		t.Fatalf("update = %d, want 200; body=%s", wu.Code, wu.Body.String())
	}
	var updated map[string]any
	json.Unmarshal(wu.Body.Bytes(), &updated)
	if updated["name"] != newName {
		t.Fatalf("name = %v, want %q", updated["name"], newName)
	}

	// Publish freezes the selector into agent_id.
	wp := httptest.NewRecorder()
	f.th.PublishTemplate(wp, withURLParam(memberRequest(t, "POST", "/api/workflow-templates/"+id+"/publish", nil), "id", id))
	if wp.Code != http.StatusOK {
		t.Fatalf("publish = %d, want 200; body=%s", wp.Code, wp.Body.String())
	}
	var published map[string]any
	json.Unmarshal(wp.Body.Bytes(), &published)
	if published["status"] != "published" {
		t.Fatalf("status = %v, want published", published["status"])
	}
	var workCfg map[string]any
	for _, n := range published["nodes"].([]any) {
		node := n.(map[string]any)
		if node["node_key"] == "work" {
			workCfg, _ = node["config"].(map[string]any)
		}
	}
	if workCfg["agent_id"] == "" || workCfg["agent_id"] == nil {
		t.Fatalf("publish did not freeze agent_id into config: %v", workCfg)
	}

	// Published templates are immutable: edit → 409, re-publish → 409.
	wu2 := httptest.NewRecorder()
	f.th.UpdateTemplate(wu2, withURLParam(memberRequest(t, "PUT", "/api/workflow-templates/"+id, map[string]any{
		"name": "nope",
	}), "id", id))
	if wu2.Code != http.StatusConflict {
		t.Fatalf("update published = %d, want 409; body=%s", wu2.Code, wu2.Body.String())
	}
	wp2 := httptest.NewRecorder()
	f.th.PublishTemplate(wp2, withURLParam(memberRequest(t, "POST", "/api/workflow-templates/"+id+"/publish", nil), "id", id))
	if wp2.Code != http.StatusConflict {
		t.Fatalf("re-publish = %d, want 409; body=%s", wp2.Code, wp2.Body.String())
	}

	// Archive.
	wa := httptest.NewRecorder()
	f.th.ArchiveTemplate(wa, withURLParam(memberRequest(t, "POST", "/api/workflow-templates/"+id+"/archive", nil), "id", id))
	if wa.Code != http.StatusOK {
		t.Fatalf("archive = %d, want 200; body=%s", wa.Code, wa.Body.String())
	}
	var archived map[string]any
	json.Unmarshal(wa.Body.Bytes(), &archived)
	if archived["status"] != "archived" {
		t.Fatalf("status = %v, want archived", archived["status"])
	}
}

// TestWorkflowTemplateAPIValidation covers the 4xx mapping: malformed graph
// → 400, missing fields → 400, unknown selector → 400 at publish, shared
// evaluator/executor agent → 422 at publish, unknown template → 404.
func TestWorkflowTemplateAPIValidation(t *testing.T) {
	f := setupTemplateAPI(t)
	suffix := time.Now().UnixNano()

	t.Run("create requires key and name", func(t *testing.T) {
		w := httptest.NewRecorder()
		f.th.CreateTemplate(w, memberRequest(t, "POST", "/api/workflow-templates", map[string]any{"name": "x"}))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("missing key = %d, want 400", w.Code)
		}
	})

	t.Run("create rejects malformed graph", func(t *testing.T) {
		w := httptest.NewRecorder()
		f.th.CreateTemplate(w, memberRequest(t, "POST", "/api/workflow-templates", map[string]any{
			"key": fmt.Sprintf("api-badgraph-%d", suffix), "name": "bad",
			// Two nodes, no edge — chain cannot cover all nodes.
			"nodes": []map[string]any{
				{"node_key": "a", "type": "end", "name": "a"},
				{"node_key": "b", "type": "end", "name": "b"},
			},
			"edges": []map[string]any{},
		}))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("bad graph = %d, want 400; body=%s", w.Code, w.Body.String())
		}
	})

	t.Run("create rejects malformed node config", func(t *testing.T) {
		w := httptest.NewRecorder()
		f.th.CreateTemplate(w, memberRequest(t, "POST", "/api/workflow-templates", map[string]any{
			"key": fmt.Sprintf("api-badcfg-%d", suffix), "name": "badcfg",
			"nodes": []map[string]any{
				{"node_key": "a", "type": "agent", "name": "a",
					"config": map[string]any{"role": "bogus"}},
			},
			"edges": []map[string]any{},
		}))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("bad config = %d, want 400; body=%s", w.Code, w.Body.String())
		}
	})

	t.Run("publish with unknown agent selector", func(t *testing.T) {
		graph := map[string]any{
			"nodes": []map[string]any{
				{"node_key": "work", "type": "agent", "name": "Work",
					"config": map[string]any{"role": "executor", "agent_selector": "No Such Agent XYZ"}},
				{"node_key": "end", "type": "end", "name": "End", "config": map[string]any{}},
			},
			"edges": []map[string]any{{"from_node_key": "work", "to_node_key": "end"}},
		}
		graph["key"] = fmt.Sprintf("api-noagent-%d", suffix)
		graph["name"] = "noagent"
		created := f.createViaAPI(t, graph)
		w := httptest.NewRecorder()
		f.th.PublishTemplate(w, withURLParam(memberRequest(t, "POST", "/api/workflow-templates/"+created["id"].(string)+"/publish", nil), "id", created["id"].(string)))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("unknown selector = %d, want 400; body=%s", w.Code, w.Body.String())
		}
	})

	t.Run("publish rejects shared evaluator agent", func(t *testing.T) {
		// Executor and evaluator both select the seeded agent → produce/review
		// separation violation (blueprint pillar 5) → 422.
		graph := map[string]any{
			"nodes": []map[string]any{
				{"node_key": "work", "type": "agent", "name": "Work",
					"config": map[string]any{"role": "executor", "agent_selector": "Handler Test Agent"}},
				{"node_key": "gate", "type": "agent", "name": "Gate",
					"config": map[string]any{"role": "evaluator", "agent_selector": "Handler Test Agent"}},
				{"node_key": "end", "type": "end", "name": "End", "config": map[string]any{}},
			},
			"edges": []map[string]any{
				{"from_node_key": "work", "to_node_key": "gate"},
				{"from_node_key": "gate", "to_node_key": "end"},
			},
		}
		graph["key"] = fmt.Sprintf("api-shared-%d", suffix)
		graph["name"] = "shared"
		created := f.createViaAPI(t, graph)
		w := httptest.NewRecorder()
		f.th.PublishTemplate(w, withURLParam(memberRequest(t, "POST", "/api/workflow-templates/"+created["id"].(string)+"/publish", nil), "id", created["id"].(string)))
		if w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("shared evaluator = %d, want 422; body=%s", w.Code, w.Body.String())
		}
	})

	t.Run("unknown template is 404", func(t *testing.T) {
		missing := "00000000-0000-0000-0000-000000000042"
		w := httptest.NewRecorder()
		f.th.GetTemplate(w, withURLParam(memberRequest(t, "GET", "/api/workflow-templates/"+missing, nil), "id", missing))
		if w.Code != http.StatusNotFound {
			t.Fatalf("get missing = %d, want 404", w.Code)
		}
	})

	t.Run("malformed id is 400", func(t *testing.T) {
		w := httptest.NewRecorder()
		f.th.GetTemplate(w, withURLParam(memberRequest(t, "GET", "/api/workflow-templates/not-a-uuid", nil), "id", "not-a-uuid"))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("bad id = %d, want 400", w.Code)
		}
	})

	t.Run("graph replace needs nodes and edges together", func(t *testing.T) {
		graph := simpleGraph()
		graph["key"] = fmt.Sprintf("api-halfgraph-%d", suffix)
		graph["name"] = "half"
		created := f.createViaAPI(t, graph)
		w := httptest.NewRecorder()
		f.th.UpdateTemplate(w, withURLParam(memberRequest(t, "PUT", "/api/workflow-templates/"+created["id"].(string), map[string]any{
			"nodes": []map[string]any{},
		}), "id", created["id"].(string)))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("half graph = %d, want 400; body=%s", w.Code, w.Body.String())
		}
	})
}

// TestWorkflowTemplateAPIGraphReplace verifies a draft's graph can be
// rewritten wholesale via PUT and the new graph is what publish freezes.
func TestWorkflowTemplateAPIGraphReplace(t *testing.T) {
	f := setupTemplateAPI(t)
	suffix := time.Now().UnixNano()

	graph := simpleGraph()
	graph["key"] = fmt.Sprintf("api-replace-%d", suffix)
	graph["name"] = "replace"
	created := f.createViaAPI(t, graph)
	id := created["id"].(string)

	// Replace with a three-node chain.
	replacement := map[string]any{
		"nodes": []map[string]any{
			{"node_key": "plan", "type": "agent", "name": "Plan",
				"config": map[string]any{"role": "executor", "agent_selector": "Handler Test Agent"}},
			{"node_key": "accept", "type": "acceptance", "name": "Accept", "config": map[string]any{}},
			{"node_key": "end", "type": "end", "name": "End", "config": map[string]any{}},
		},
		"edges": []map[string]any{
			{"from_node_key": "plan", "to_node_key": "accept"},
			{"from_node_key": "accept", "to_node_key": "end"},
		},
	}
	w := httptest.NewRecorder()
	f.th.UpdateTemplate(w, withURLParam(memberRequest(t, "PUT", "/api/workflow-templates/"+id, replacement), "id", id))
	if w.Code != http.StatusOK {
		t.Fatalf("replace = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var detail map[string]any
	json.Unmarshal(w.Body.Bytes(), &detail)
	if nodes, _ := detail["nodes"].([]any); len(nodes) != 3 {
		t.Fatalf("nodes after replace = %d, want 3", len(nodes))
	}
	if edges, _ := detail["edges"].([]any); len(edges) != 2 {
		t.Fatalf("edges after replace = %d, want 2", len(edges))
	}

	// Publish still succeeds against the replaced graph.
	wp := httptest.NewRecorder()
	f.th.PublishTemplate(wp, withURLParam(memberRequest(t, "POST", "/api/workflow-templates/"+id+"/publish", nil), "id", id))
	if wp.Code != http.StatusOK {
		t.Fatalf("publish after replace = %d, want 200; body=%s", wp.Code, wp.Body.String())
	}
}
