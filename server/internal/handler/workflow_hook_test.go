package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/workflow"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/featureflag"
)

// workflow_hook_test.go — API contract tests for the inbound workflow hook
// (public ingress) and the hook management API. DB-backed via the shared
// TestMain fixture; requests to the ingress carry no auth headers at all (the
// URL token IS the credential), management requests carry an injected
// workspace-member context (what RequireWorkspaceMember would stamp).

// hookFixture bundles a published template + hook row with a KNOWN cleartext
// token (only the hash is persisted) + the handler under test.
type hookFixture struct {
	hh          *WorkflowHookHandler
	templateID  string
	hookID      string
	token       string
	executorID  string
	evaluatorID string
}

// setupHookFixture creates executor/evaluator agents (unique names — the
// selector resolution is by name in the shared workspace), publishes the
// given template, and mints a hook bound to it. Rate limiters are nil (the
// limiter contract itself is covered by webhook_rate_limiter_test.go).
func setupHookFixture(t *testing.T, key string, nodes []workflow.NodeInput, edges []workflow.EdgeInput) *hookFixture {
	t.Helper()
	ctx := context.Background()
	suffix := time.Now().UnixNano()

	f := &hookFixture{token: fmt.Sprintf("wfh_testtoken_%d", suffix)}
	mkAgent := func(role string) string {
		var id string
		if err := testPool.QueryRow(ctx, `
			INSERT INTO agent (
				workspace_id, name, description, runtime_mode, runtime_config,
				runtime_id, visibility, max_concurrent_tasks, owner_id
			) VALUES ($1, $2, '', 'cloud', '{}'::jsonb, $3, 'private', 1, $4)
			RETURNING id
		`, testWorkspaceID, fmt.Sprintf("WF Hook %s %d", role, suffix), testRuntimeID, testUserID).Scan(&id); err != nil {
			t.Fatalf("create %s agent: %v", role, err)
		}
		return id
	}
	f.executorID = mkAgent("Executor")
	f.evaluatorID = mkAgent("Evaluator")

	for i := range nodes {
		cfg, err := workflow.ParseNodeConfig(nodes[i].Config)
		if err != nil {
			t.Fatalf("parse node config: %v", err)
		}
		// P1-3b: NodeTypeGate + gate_type=agent/adversarial binds to the
		// evaluator agent (activateGateAgentNode forces role=evaluator).
		isAgentGate := nodes[i].Type == workflow.NodeTypeGate &&
			(cfg.GateType == workflow.GateTypeAgent || cfg.GateType == workflow.GateTypeAdversarial)
		bindEvaluator := cfg.Role == workflow.RoleEvaluator || isAgentGate
		switch {
		case isAgentGate || nodes[i].Type == workflow.NodeTypeAgent:
			if bindEvaluator {
				cfg.AgentSelector = fmt.Sprintf("WF Hook Evaluator %d", suffix)
			} else {
				cfg.AgentSelector = fmt.Sprintf("WF Hook Executor %d", suffix)
			}
		}
		raw, _ := json.Marshal(cfg)
		nodes[i].Config = raw
	}

	templates := workflow.NewTemplateService(testHandler.Queries, testPool)
	detail, err := templates.CreateTemplate(ctx, workflow.CreateTemplateParams{
		WorkspaceID: util.MustParseUUID(testWorkspaceID),
		Key:         fmt.Sprintf("%s-%d", key, suffix),
		Name:        key,
		CreatedBy:   util.MustParseUUID(testUserID),
		Nodes:       nodes,
		Edges:       edges,
	})
	if err != nil {
		t.Fatalf("create template: %v", err)
	}
	published, err := templates.PublishTemplate(ctx, util.MustParseUUID(testWorkspaceID), detail.Template.ID)
	if err != nil {
		t.Fatalf("publish template: %v", err)
	}
	f.templateID = uuidToString(published.Template.ID)

	hook, err := testHandler.Queries.CreateWorkflowHook(ctx, db.CreateWorkflowHookParams{
		WorkspaceID: util.MustParseUUID(testWorkspaceID),
		TemplateID:  published.Template.ID,
		TokenHash:   hashHookToken(f.token),
		Name:        "test hook",
	})
	if err != nil {
		t.Fatalf("create hook: %v", err)
	}
	f.hookID = uuidToString(hook.ID)

	provider := featureflag.NewStaticProvider()
	provider.Set(workflow.FlagEngine, featureflag.Rule{Default: true})
	engine := workflow.NewEngine(testHandler.Queries, testPool, testHandler.IssueService, testHandler.TaskService, events.New())
	f.hh = NewWorkflowHookHandler(testHandler.Queries, engine, featureflag.NewService(provider), nil, nil, nil)

	t.Cleanup(func() {
		ctx := context.Background()
		testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE agent_id IN ($1, $2)`, f.executorID, f.evaluatorID)
		testPool.Exec(ctx, `DELETE FROM workflow_hook WHERE id = $1`, f.hookID)
		// workflow_run.template_id is FK RESTRICT: runs (and their issues)
		// go before the template row.
		testPool.Exec(ctx, `DELETE FROM issue WHERE parent_issue_id IN (SELECT intake_issue_id FROM workflow_run WHERE workspace_id = $1)`, testWorkspaceID)
		testPool.Exec(ctx, `DELETE FROM issue WHERE id IN (SELECT intake_issue_id FROM workflow_run WHERE workspace_id = $1)`, testWorkspaceID)
		testPool.Exec(ctx, `DELETE FROM workflow_run WHERE workspace_id = $1`, testWorkspaceID)
		testPool.Exec(ctx, `DELETE FROM workflow_template WHERE id = $1`, f.templateID)
		testPool.Exec(ctx, `DELETE FROM inbox_item WHERE workspace_id = $1 AND type LIKE 'workflow_%'`, testWorkspaceID)
		testPool.Exec(ctx, `DELETE FROM agent WHERE id IN ($1, $2)`, f.executorID, f.evaluatorID)
	})
	return f
}

// hookRequest builds an UNAUTHENTICATED ingress request carrying the URL
// token as a chi param (what the public route sees).
func hookRequest(t *testing.T, token string, body any) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if raw, ok := body.(string); ok {
			buf.WriteString(raw)
		} else if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req := httptest.NewRequest("POST", "/api/hooks/workflow/"+token, &buf)
	req.Header.Set("Content-Type", "application/json")
	return withURLParam(req, "token", token)
}

// memberRequest stamps the workspace-member context that
// RequireWorkspaceMember would inject (management API requests).
func memberRequest(t *testing.T, method, path string, body any) *http.Request {
	t.Helper()
	req := newRequest(method, path, body)
	member, err := testHandler.Queries.GetMemberByUserAndWorkspace(context.Background(), db.GetMemberByUserAndWorkspaceParams{
		UserID:      util.MustParseUUID(testUserID),
		WorkspaceID: util.MustParseUUID(testWorkspaceID),
	})
	if err != nil {
		t.Fatalf("load test member: %v", err)
	}
	return req.WithContext(middleware.SetMemberContext(req.Context(), testWorkspaceID, member))
}

func decodeHookResponse(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, w.Body.String())
	}
	return resp
}

func countRunsForSource(t *testing.T, sourceID string) int {
	t.Helper()
	var n int
	if err := testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM workflow_run WHERE workspace_id = $1 AND source_type = 'hook' AND source_id = $2`,
		testWorkspaceID, sourceID).Scan(&n); err != nil {
		t.Fatalf("count runs: %v", err)
	}
	return n
}

// TestWorkflowHookIngressCreatesRun: one valid push → 201 + run_id + intake
// issue number; the run carries source_type=hook / source_id and the
// reviewer resolved from the member email; last_used_at is bumped (P0
// delivery audit, design.md §1).
func TestWorkflowHookIngressCreatesRun(t *testing.T) {
	nodes, edges := twoNodeTemplate(t, false)
	f := setupHookFixture(t, "hook-create", nodes, edges)
	sourceID := fmt.Sprintf("ext-%d", time.Now().UnixNano())

	w := httptest.NewRecorder()
	f.hh.HandleInboundHook(w, hookRequest(t, f.token, map[string]any{
		"title":      "Inbound work item",
		"source_id":  sourceID,
		"reviewer":   handlerTestEmail,
		"source_url": "https://tracker.example/items/1",
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	resp := decodeHookResponse(t, w)
	if resp["created"] != true {
		t.Fatalf("created = %v, want true", resp["created"])
	}
	runID, _ := resp["run_id"].(string)
	if runID == "" {
		t.Fatalf("run_id missing in %v", resp)
	}
	if n, _ := resp["issue_number"].(float64); n <= 0 {
		t.Fatalf("issue_number = %v, want > 0", resp["issue_number"])
	}

	// Reviewer resolved from the member email into the run context.
	var rawCtx []byte
	if err := testPool.QueryRow(context.Background(),
		`SELECT context FROM workflow_run WHERE id = $1`, runID).Scan(&rawCtx); err != nil {
		t.Fatalf("read run context: %v", err)
	}
	rc := workflow.ParseRunContext(rawCtx)
	if !rc.Reviewer().Valid {
		t.Fatalf("reviewer not resolved into run context: %s", rawCtx)
	}

	// Delivery audit: last_used_at bumped.
	hook, err := testHandler.Queries.GetWorkflowHook(context.Background(), util.MustParseUUID(f.hookID))
	if err != nil {
		t.Fatalf("reload hook: %v", err)
	}
	if !hook.LastUsedAt.Valid {
		t.Fatalf("last_used_at not bumped")
	}
}

// TestWorkflowHookIdempotentReplay: a duplicate push (same source_id +
// template) returns 200 with the EXISTING run and creates nothing new
// (blueprint §8.3; UNIQUE constraint is the backstop).
func TestWorkflowHookIdempotentReplay(t *testing.T) {
	nodes, edges := twoNodeTemplate(t, false)
	f := setupHookFixture(t, "hook-replay", nodes, edges)
	sourceID := fmt.Sprintf("ext-%d", time.Now().UnixNano())
	payload := map[string]any{"title": "Replay me", "source_id": sourceID}

	w1 := httptest.NewRecorder()
	f.hh.HandleInboundHook(w1, hookRequest(t, f.token, payload))
	if w1.Code != http.StatusCreated {
		t.Fatalf("first push = %d, want 201; body=%s", w1.Code, w1.Body.String())
	}
	first := decodeHookResponse(t, w1)

	w2 := httptest.NewRecorder()
	f.hh.HandleInboundHook(w2, hookRequest(t, f.token, payload))
	if w2.Code != http.StatusOK {
		t.Fatalf("replay = %d, want 200; body=%s", w2.Code, w2.Body.String())
	}
	second := decodeHookResponse(t, w2)
	if second["created"] != false {
		t.Fatalf("replay created = %v, want false", second["created"])
	}
	if second["run_id"] != first["run_id"] {
		t.Fatalf("replay run_id = %v, want original %v", second["run_id"], first["run_id"])
	}
	if n := countRunsForSource(t, sourceID); n != 1 {
		t.Fatalf("runs for source = %d, want exactly 1", n)
	}
}

// TestWorkflowHookBadRequests: the payload validation matrix (design.md §3) —
// missing source_id/title, unresolvable reviewer, unknown template_key,
// malformed JSON, disabled hook, unknown token, flag-off.
func TestWorkflowHookBadRequests(t *testing.T) {
	nodes, edges := twoNodeTemplate(t, false)
	f := setupHookFixture(t, "hook-400s", nodes, edges)

	cases := []struct {
		name string
		body any
		want int
	}{
		{"missing source_id", map[string]any{"title": "x"}, http.StatusBadRequest},
		{"blank source_id", map[string]any{"title": "x", "source_id": "  "}, http.StatusBadRequest},
		{"missing title", map[string]any{"source_id": "x"}, http.StatusBadRequest},
		{"unknown reviewer email", map[string]any{"title": "x", "source_id": "x", "reviewer": "nobody@example.com"}, http.StatusBadRequest},
		{"unknown reviewer id", map[string]any{"title": "x", "source_id": "x", "reviewer": "00000000-0000-0000-0000-000000000099"}, http.StatusBadRequest},
		{"unknown template_key", map[string]any{"title": "x", "source_id": "x", "template_key": "no-such-template"}, http.StatusBadRequest},
		{"malformed json", "{", http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			f.hh.HandleInboundHook(w, hookRequest(t, f.token, tc.body))
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d; body=%s", w.Code, tc.want, w.Body.String())
			}
		})
	}

	t.Run("unknown token", func(t *testing.T) {
		w := httptest.NewRecorder()
		f.hh.HandleInboundHook(w, hookRequest(t, "wfh_no_such_token", map[string]any{"title": "x", "source_id": "x"}))
		if w.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404; body=%s", w.Code, w.Body.String())
		}
	})

	t.Run("disabled hook", func(t *testing.T) {
		if _, err := testHandler.Queries.SetWorkflowHookStatus(context.Background(), db.SetWorkflowHookStatusParams{
			ID: util.MustParseUUID(f.hookID), Status: "disabled", WorkspaceID: util.MustParseUUID(testWorkspaceID),
		}); err != nil {
			t.Fatalf("disable hook: %v", err)
		}
		w := httptest.NewRecorder()
		f.hh.HandleInboundHook(w, hookRequest(t, f.token, map[string]any{"title": "x", "source_id": "x"}))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401; body=%s", w.Code, w.Body.String())
		}
	})

	t.Run("flag off is 404", func(t *testing.T) {
		provider := featureflag.NewStaticProvider() // flag unset → default off
		engine := workflow.NewEngine(testHandler.Queries, testPool, testHandler.IssueService, testHandler.TaskService, events.New())
		hh := NewWorkflowHookHandler(testHandler.Queries, engine, featureflag.NewService(provider), nil, nil, nil)
		w := httptest.NewRecorder()
		hh.HandleInboundHook(w, hookRequest(t, f.token, map[string]any{"title": "x", "source_id": "x"}))
		if w.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404 (flag off); body=%s", w.Code, w.Body.String())
		}
	})
}

// TestWorkflowHookTemplateKeyOverride: the payload's template_key resolves to
// the newest published version in the workspace instead of the hook's bound
// template.
func TestWorkflowHookTemplateKeyOverride(t *testing.T) {
	nodes, edges := twoNodeTemplate(t, false)
	f := setupHookFixture(t, "hook-bound", nodes, edges)

	// A second published template the hook is NOT bound to, with selectors
	// aimed at the fixture's executor agent by name.
	otherNodes, otherEdges := twoNodeTemplate(t, false)
	suffix := time.Now().UnixNano()
	var agentName string
	if err := testPool.QueryRow(context.Background(),
		`SELECT name FROM agent WHERE id = $1`, f.executorID).Scan(&agentName); err != nil {
		t.Fatalf("read agent name: %v", err)
	}
	for i := range otherNodes {
		cfg, _ := workflow.ParseNodeConfig(otherNodes[i].Config)
		cfg.AgentSelector = agentName
		raw, _ := json.Marshal(cfg)
		otherNodes[i].Config = raw
	}
	templates := workflow.NewTemplateService(testHandler.Queries, testPool)
	otherKey := fmt.Sprintf("hook-override-%d", suffix)
	detail, err := templates.CreateTemplate(context.Background(), workflow.CreateTemplateParams{
		WorkspaceID: util.MustParseUUID(testWorkspaceID),
		Key:         otherKey,
		Name:        otherKey,
		CreatedBy:   util.MustParseUUID(testUserID),
		Nodes:       otherNodes,
		Edges:       otherEdges,
	})
	if err != nil {
		t.Fatalf("create override template: %v", err)
	}
	other, err := templates.PublishTemplate(context.Background(), util.MustParseUUID(testWorkspaceID), detail.Template.ID)
	if err != nil {
		t.Fatalf("publish override template: %v", err)
	}
	t.Cleanup(func() {
		// FK RESTRICT (run → template): this cleanup runs before the
		// fixture's, so the override run must go first.
		testPool.Exec(context.Background(), `DELETE FROM issue WHERE parent_issue_id IN (SELECT intake_issue_id FROM workflow_run WHERE template_id = $1)`, other.Template.ID)
		testPool.Exec(context.Background(), `DELETE FROM issue WHERE id IN (SELECT intake_issue_id FROM workflow_run WHERE template_id = $1)`, other.Template.ID)
		testPool.Exec(context.Background(), `DELETE FROM workflow_run WHERE template_id = $1`, other.Template.ID)
		testPool.Exec(context.Background(), `DELETE FROM workflow_template WHERE id = $1`, other.Template.ID)
	})

	sourceID := fmt.Sprintf("ext-%d", time.Now().UnixNano())
	w := httptest.NewRecorder()
	f.hh.HandleInboundHook(w, hookRequest(t, f.token, map[string]any{
		"title": "Override", "source_id": sourceID, "template_key": otherKey,
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	resp := decodeHookResponse(t, w)
	var templateID string
	if err := testPool.QueryRow(context.Background(),
		`SELECT template_id::text FROM workflow_run WHERE id = $1`, resp["run_id"]).Scan(&templateID); err != nil {
		t.Fatalf("read run template: %v", err)
	}
	if templateID != uuidToString(other.Template.ID) {
		t.Fatalf("run template = %s, want override %s", templateID, uuidToString(other.Template.ID))
	}
}

// TestWorkflowHookManagementAPI: create (cleartext token returned ONCE),
// list (never carries even the hash), disable.
func TestWorkflowHookManagementAPI(t *testing.T) {
	nodes, edges := twoNodeTemplate(t, false)
	f := setupHookFixture(t, "hook-mgmt", nodes, edges)

	// Create.
	w := httptest.NewRecorder()
	f.hh.CreateHook(w, memberRequest(t, "POST", "/api/workflow-hooks", map[string]any{
		"template_id": f.templateID,
		"name":        "mgmt hook",
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	created := decodeHookResponse(t, w)
	token, _ := created["token"].(string)
	if token == "" || token[:4] != "wfh_" {
		t.Fatalf("cleartext token missing/malformed in create response: %v", created)
	}
	if _, leaked := created["token_hash"]; leaked {
		t.Fatalf("create response must not carry token_hash")
	}
	createdID, _ := created["id"].(string)
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM workflow_hook WHERE id = $1`, createdID)
	})

	// Token works for ingress.
	sourceID := fmt.Sprintf("ext-%d", time.Now().UnixNano())
	wi := httptest.NewRecorder()
	f.hh.HandleInboundHook(wi, hookRequest(t, token, map[string]any{"title": "via mgmt token", "source_id": sourceID}))
	if wi.Code != http.StatusCreated {
		t.Fatalf("ingress with minted token = %d, want 201; body=%s", wi.Code, wi.Body.String())
	}

	// List: hooks present, no token material anywhere in the payload.
	wl := httptest.NewRecorder()
	f.hh.ListHooks(wl, memberRequest(t, "GET", "/api/workflow-hooks", nil))
	if wl.Code != http.StatusOK {
		t.Fatalf("list = %d, want 200; body=%s", wl.Code, wl.Body.String())
	}
	if bytes.Contains(wl.Body.Bytes(), []byte("token_hash")) || bytes.Contains(wl.Body.Bytes(), []byte(hashHookToken(token))) {
		t.Fatalf("list response leaks token material: %s", wl.Body.String())
	}

	// Disable → ingress 401s.
	wd := httptest.NewRecorder()
	f.hh.DisableHook(wd, withURLParam(memberRequest(t, "POST", "/api/workflow-hooks/"+createdID+"/disable", nil), "id", createdID))
	if wd.Code != http.StatusOK {
		t.Fatalf("disable = %d, want 200; body=%s", wd.Code, wd.Body.String())
	}
	w2 := httptest.NewRecorder()
	f.hh.HandleInboundHook(w2, hookRequest(t, token, map[string]any{"title": "after disable", "source_id": sourceID + "-2"}))
	if w2.Code != http.StatusUnauthorized {
		t.Fatalf("ingress after disable = %d, want 401; body=%s", w2.Code, w2.Body.String())
	}
}
