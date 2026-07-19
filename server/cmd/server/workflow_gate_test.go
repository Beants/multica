package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/realtime"
	"github.com/multica-ai/multica/server/internal/workflow"
	"github.com/multica-ai/multica/server/pkg/featureflag"
)

// workflow_gate_test.go — AC6 wiring proof: while the workflow_engine flag
// is off, the workflow routes answer 404 (indistinguishable from never being
// registered); with it on, the route exists and applies its own auth rules.
// Uses the integration TestMain fixture (testServer / testPool / testToken).

func TestWorkflowRoutesGatedByFeatureFlag(t *testing.T) {
	path := "/api/tasks/00000000-0000-0000-0000-000000000001/submission"

	// Flag OFF (the shared testServer has no flag service configured):
	// the route falls through to the router's 404.
	resp := authRequest(t, http.MethodPost, path, map[string]any{"status": "DONE"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("flag off: status = %d, want 404", resp.StatusCode)
	}

	// Flag ON: build a second router with the flag statically enabled. The
	// request carries a member JWT (not a task token), so it must pass the
	// gate and be rejected by the handler's mat_-only rule — 403, not 404.
	provider := featureflag.NewStaticProvider()
	provider.Set(workflow.FlagEngine, featureflag.Rule{Default: true})
	flags := featureflag.NewService(provider)

	hub := realtime.NewHub()
	go hub.Run()
	bus := events.New()
	router, _ := NewRouterWithOptions(testPool, hub, bus, analytics.NoopClient{}, nil, RouterOptions{
		FeatureFlags: flags,
	})
	srv := httptest.NewServer(router)
	defer srv.Close()

	req, err := http.NewRequest(http.MethodPost, srv.URL+path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp2.Body.Close()
	body, _ := io.ReadAll(resp2.Body)
	if resp2.StatusCode != http.StatusForbidden {
		t.Fatalf("flag on: status = %d, want 403 (route registered, mat_-only rule); body=%s",
			resp2.StatusCode, body)
	}
}

// TestWorkflowHumanRoutesRejectTaskToken proves the human workflow surfaces
// (runs / templates / hooks / acceptance) reject mat_ task tokens with 403.
// The subtlety being pinned: the mat_ auth branch stamps X-User-ID with the
// runtime OWNER's user id, so the token sails through RequireWorkspaceMember
// as the owner — only the RequireHumanActor gate on those route groups keeps
// an executor agent from approving its own acceptance or minting hook tokens
// (design.md §3: human APIs are session/PAT-only). The member JWT must still
// work, and the same mat_ token must still reach its agent surface.
func TestWorkflowHumanRoutesRejectTaskToken(t *testing.T) {
	ctx := context.Background()

	var agentID string
	if err := testPool.QueryRow(ctx,
		`SELECT id FROM agent WHERE workspace_id = $1 AND name = 'Integration Test Agent'`,
		testWorkspaceID).Scan(&agentID); err != nil {
		t.Fatalf("resolve integration agent: %v", err)
	}
	var runtimeID string
	if err := testPool.QueryRow(ctx,
		`SELECT id FROM agent_runtime WHERE workspace_id = $1 AND name = 'Integration Test Runtime'`,
		testWorkspaceID).Scan(&runtimeID); err != nil {
		t.Fatalf("resolve integration runtime: %v", err)
	}
	var taskID string
	if err := testPool.QueryRow(ctx,
		`INSERT INTO agent_task_queue (agent_id, runtime_id) VALUES ($1, $2) RETURNING id`,
		agentID, runtimeID).Scan(&taskID); err != nil {
		t.Fatalf("create task: %v", err)
	}
	token := fmt.Sprintf("mat_gate_test_%d", time.Now().UnixNano())
	if _, err := testPool.Exec(ctx, `
		INSERT INTO task_token (token_hash, task_id, agent_id, workspace_id, user_id, expires_at)
		VALUES ($1, $2, $3, $4, $5, now() + interval '1 hour')
	`, auth.HashToken(token), taskID, agentID, testWorkspaceID, testUserID); err != nil {
		t.Fatalf("mint task token: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM task_token WHERE task_id = $1`, taskID)
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
	})

	provider := featureflag.NewStaticProvider()
	provider.Set(workflow.FlagEngine, featureflag.Rule{Default: true})
	hub := realtime.NewHub()
	go hub.Run()
	router, _ := NewRouterWithOptions(testPool, hub, events.New(), analytics.NoopClient{}, nil, RouterOptions{
		FeatureFlags: featureflag.NewService(provider),
	})
	srv := httptest.NewServer(router)
	defer srv.Close()

	do := func(method, path, bearer string) int {
		req, err := http.NewRequest(method, srv.URL+path, nil)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+bearer)
		if bearer == testToken {
			// Member JWTs resolve the workspace from the header; mat_
			// tokens get it stamped from the token binding instead.
			req.Header.Set("X-Workspace-ID", testWorkspaceID)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request %s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	// Every human surface 403s the task token.
	for _, tc := range [][2]string{
		{http.MethodGet, "/api/workflow-runs"},
		{http.MethodPost, "/api/workflow-templates"},
		{http.MethodGet, "/api/workflow-hooks"},
		{http.MethodPost, "/api/workflow-runs/00000000-0000-0000-0000-000000000001/acceptance/approve"},
	} {
		if code := do(tc[0], tc[1], token); code != http.StatusForbidden {
			t.Fatalf("mat_ %s %s = %d, want 403 (RequireHumanActor)", tc[0], tc[1], code)
		}
	}
	// The human member JWT still reaches the surface.
	if code := do(http.MethodGet, "/api/workflow-runs", testToken); code != http.StatusOK {
		t.Fatalf("member GET /api/workflow-runs = %d, want 200", code)
	}
	// The same mat_ token still reaches its AGENT surface (no actor gate on
	// /api/tasks/*): the handler's step binding 404s, proving auth passed.
	subReq, err := http.NewRequest(http.MethodPost, srv.URL+"/api/tasks/"+taskID+"/submission",
		strings.NewReader(`{"status":"DONE"}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	subReq.Header.Set("Authorization", "Bearer "+token)
	subReq.Header.Set("Content-Type", "application/json")
	subResp, err := http.DefaultClient.Do(subReq)
	if err != nil {
		t.Fatalf("submission request: %v", err)
	}
	defer subResp.Body.Close()
	if subResp.StatusCode != http.StatusNotFound {
		t.Fatalf("mat_ POST submission = %d, want 404 (no step bound — actor gates passed)", subResp.StatusCode)
	}
}
