package handler

// workflow_fanout_e2e_test.go — P1-1 Wave 3 acceptance-criteria suite
// (implement.md step 3.1-3.4). One test per AC, exercised through the
// HTTP handler surface: hook ingress → executor submission → evaluator
// verdict → converge. Mirrors the P0 e2e_ac_test.go pattern but drives
// the canonical P1-1 fan_out → branch → converge shape.
//
// Wave 2 already pins the same semantics at the engine package layer
// (internal/workflow/{fanout,converge}_test.go). Wave 3's value-add is
// the HTTP e2e proof: the wiring from POST /submission all the way to
// run state + inbox + downstream activation, plus the publish-time and
// flag-off surfaces the engine tests cannot reach.
//
// IMPLEMENTATION REALITY (PRD vs Wave 2 design):
// PRD AC4-AC6 phrase fan_out item-level failures as "submission 422
// reject", but Wave 2 runs fan_out validation in the activation phase
// (post-commit, after the upstream submission has returned 201). So
// the observable behavior on the HTTP surface is:
//   - The upstream /submission itself returns 201 (its schema is
//     valid — subtasks is just an array).
//   - fan_out activation fails → failActivation lands the fan_out
//     step in 'blocked' and the run in 'paused'/'failed', with an
//     inbox notification to the initiator (AC4-AC6).
//   - AC13 (upstream submission omits items_field entirely) hits
//     the P0 Layer-1 schema validation when the upstream declares
//     the items_field as required → /submission returns 422 directly.
// Both behaviors are pinned below; the gap with the PRD wording is
// documented in design.md §5 / §6 (PRD-vs-impl tradeoff accepted at
// Wave 2 review).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/workflow"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/featureflag"
)

// ---------------------------------------------------------------------------
// Template helpers
// ---------------------------------------------------------------------------

// fanOutE2ETemplate builds the canonical P1-1 fan_out shape with a
// parameterizable fail_policy and branch role:
//
//	upstream(executor, declares "subtasks" array required) → fan_out
//	→ branch(role=branchRole) → converge → end
//
// branchRole=RoleExecutor: branch agents get system-derived pass
// verdicts on DONE submissions (AC3, AC10, expand happy path).
// branchRole=RoleEvaluator: branch agents issue verdicts directly,
// enabling fail verdicts (AC7-AC9).
//
// fan_out's fail_policy is set as given; converge has no config.
func fanOutE2ETemplate(t *testing.T, failPolicy, branchRole string) ([]workflow.NodeInput, []workflow.EdgeInput) {
	t.Helper()
	if branchRole == "" {
		branchRole = workflow.RoleExecutor
	}
	upstream := workflow.NodeInput{
		NodeKey: "upstream", Type: workflow.NodeTypeAgent, Name: "upstream",
		Config: agentCfg(t, workflow.NodeConfig{
			Role:          workflow.RoleExecutor,
			AgentSelector: "WF Placeholder Executor",
			Instructions:  "Plan and emit subtasks",
			ExitFields: &workflow.ExitFieldsSchema{Fields: []workflow.ExitFieldSpec{
				{Name: "subtasks", Type: "array", Required: true},
			}},
		}),
	}
	fanOut := workflow.NodeInput{
		NodeKey: "fanout", Type: workflow.NodeTypeFanOut, Name: "fanout",
		Config: agentCfg(t, workflow.NodeConfig{
			ItemsField: "subtasks",
			FailPolicy: failPolicy,
		}),
	}
	branch := workflow.NodeInput{
		NodeKey: "branch", Type: workflow.NodeTypeAgent, Name: "branch",
		Config: agentCfg(t, workflow.NodeConfig{
			Role:          branchRole,
			AgentSelector: "WF Placeholder Executor",
			Instructions:  "Execute one fan_out child task",
		}),
	}
	converge := workflow.NodeInput{
		NodeKey: "converge", Type: workflow.NodeTypeConverge, Name: "converge",
		Config: agentCfg(t, workflow.NodeConfig{}),
	}
	end := workflow.NodeInput{
		NodeKey: "end", Type: workflow.NodeTypeEnd, Name: "end",
		Config: agentCfg(t, workflow.NodeConfig{}),
	}
	return []workflow.NodeInput{upstream, fanOut, branch, converge, end}, []workflow.EdgeInput{
		{FromNodeKey: "upstream", ToNodeKey: "fanout"},
		{FromNodeKey: "fanout", ToNodeKey: "branch"},
		{FromNodeKey: "branch", ToNodeKey: "converge"},
		{FromNodeKey: "converge", ToNodeKey: "end"},
	}
}

// postFanOutSubmission records the upstream DONE submission carrying a
// subtasks array. Returns the HTTP recorder for the caller to inspect.
func postFanOutSubmission(t *testing.T, wh *WorkflowHandler, taskID, agentID string, items []any) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	wh.CreateSubmission(w, matRequest(t, "POST", "/api/tasks/"+taskID+"/submission", taskID, agentID, map[string]any{
		"status": workflow.SubmissionDone,
		"exit_fields": map[string]any{
			"subtasks": items,
		},
	}))
	return w
}

// postVerdictWithResult records an evaluator verdict with an explicit
// result (pass/fail/blocked). Used by AC7-AC9 to drive fail_policy.
func postVerdictWithResult(t *testing.T, wh *WorkflowHandler, taskID, agentID, result string, exitFields map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	wh.CreateVerdict(w, matRequest(t, "POST", "/api/tasks/"+taskID+"/verdict", taskID, agentID, map[string]any{
		"result":      result,
		"exit_fields": exitFields,
	}))
	return w
}

// childTasksForRun returns the agent task IDs of every fan_out child
// step under the "branch" node (parent_step_id IS NOT NULL), ordered
// by attempt ascending (which is also child-index order because the
// slot encoding is i*childAttemptSlot+1).
func childTasksForRun(t *testing.T, runID string) []string {
	t.Helper()
	rows, err := testPool.Query(context.Background(), `
		SELECT agent_task_id::text FROM step_instance
		WHERE run_id = $1 AND node_key = 'branch' AND parent_step_id IS NOT NULL
		ORDER BY attempt ASC
	`, runID)
	if err != nil {
		t.Fatalf("query child tasks: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan child task: %v", err)
		}
		out = append(out, id)
	}
	return out
}

// childStepStatuses returns the latest step status per fan_out slot
// (one entry per child) for inspection. The "latest per slot" rule
// matches the converge tally (engine.go): rework attempts within a
// slot don't double-count.
func childStepStatuses(t *testing.T, runID string) []string {
	t.Helper()
	rows, err := testPool.Query(context.Background(), `
		SELECT DISTINCT ON (attempt / 1024) status
		FROM step_instance
		WHERE run_id = $1 AND node_key = 'branch' AND parent_step_id IS NOT NULL
		ORDER BY attempt / 1024, attempt DESC
	`, runID)
	if err != nil {
		t.Fatalf("query child statuses: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan child status: %v", err)
		}
		out = append(out, s)
	}
	return out
}

// childStepAttempts returns every fan_out child step's attempt number
// (across all retries). Used by AC9 to assert rework increments the
// failed child's attempt without touching siblings.
func childStepAttempts(t *testing.T, runID string) []int32 {
	t.Helper()
	rows, err := testPool.Query(context.Background(), `
		SELECT attempt FROM step_instance
		WHERE run_id = $1 AND node_key = 'branch' AND parent_step_id IS NOT NULL
		ORDER BY attempt ASC
	`, runID)
	if err != nil {
		t.Fatalf("query child attempts: %v", err)
	}
	defer rows.Close()
	var out []int32
	for rows.Next() {
		var a int32
		if err := rows.Scan(&a); err != nil {
			t.Fatalf("scan attempt: %v", err)
		}
		out = append(out, a)
	}
	return out
}

// stepStatusForNodeKey returns the latest step status for a node in
// the run.
func stepStatusForNodeKey(t *testing.T, runID, nodeKey string) string {
	t.Helper()
	return stepStatusForRun(t, runID, nodeKey)
}

// countInboxType counts inbox_item rows for the run's workspace of
// the given type (e.g. "workflow_blocked", "workflow_escalated").
func countInboxType(t *testing.T, typ string) int {
	t.Helper()
	var n int
	if err := testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM inbox_item WHERE workspace_id = $1 AND type = $2`,
		testWorkspaceID, typ).Scan(&n); err != nil {
		t.Fatalf("count inbox %s: %v", typ, err)
	}
	return n
}

// validFanOutItem returns a subtask item map that parses cleanly and
// resolves to the given agent name. Tests pass the freshly-created
// fixture executor name (the same agent the upstream/branch binds to).
func validFanOutItem(title, agentName string) map[string]any {
	return map[string]any{
		"title":          title,
		"instructions":   "do " + title,
		"agent_selector": agentName,
		"priority":       "medium",
	}
}

// executorAgentName reads the fixture's executor agent name back so
// fan_out subtask items can resolve. Mirrors setupWorkflowAPIFixture's
// auto-bind ("WF Executor <suffix>").
func executorAgentName(agentID string) string {
	var name string
	if err := testPool.QueryRow(context.Background(),
		`SELECT name FROM agent WHERE id = $1`, agentID).Scan(&name); err != nil {
		return ""
	}
	return name
}

// ---------------------------------------------------------------------------
// AC1: publish-time validation — fan_out config invariants
// ---------------------------------------------------------------------------

// TestAC1FanOutPublishValidation covers the three publish-time
// refusals from PRD AC1 / R6: missing items_field, items_field
// pointing at a non-array upstream field, and illegal fail_policy.
func TestAC1FanOutPublishValidation(t *testing.T) {
	ctx := context.Background()

	publish := func(nodes []workflow.NodeInput, edges []workflow.EdgeInput) error {
		templates := workflow.NewTemplateService(testHandler.Queries, testPool)
		detail, err := templates.CreateTemplate(ctx, workflow.CreateTemplateParams{
			WorkspaceID: util.MustParseUUID(testWorkspaceID),
			Key:         fmt.Sprintf("ac1-publish-%d", time.Now().UnixNano()),
			Name:        "ac1",
			CreatedBy:   util.MustParseUUID(testUserID),
			Nodes:       nodes, Edges: edges,
		})
		if err != nil {
			return err
		}
		_, perr := templates.PublishTemplate(ctx, util.MustParseUUID(testWorkspaceID), detail.Template.ID)
		return perr
	}

	upstreamWith := func(fieldName, fieldType string) workflow.NodeInput {
		return workflow.NodeInput{
			NodeKey: "upstream", Type: workflow.NodeTypeAgent, Name: "upstream",
			Config: agentCfg(t, workflow.NodeConfig{
				Role:          workflow.RoleExecutor,
				AgentSelector: "WF Placeholder Executor",
				ExitFields: &workflow.ExitFieldsSchema{Fields: []workflow.ExitFieldSpec{
					{Name: fieldName, Type: fieldType},
				}},
			}),
		}
	}
	agentNode := func(key string) workflow.NodeInput {
		return workflow.NodeInput{NodeKey: key, Type: workflow.NodeTypeAgent, Name: key,
			Config: agentCfg(t, workflow.NodeConfig{Role: workflow.RoleExecutor, AgentSelector: "WF Placeholder Executor"})}
	}
	endNode := func() workflow.NodeInput {
		return workflow.NodeInput{NodeKey: "end", Type: workflow.NodeTypeEnd, Name: "end",
			Config: agentCfg(t, workflow.NodeConfig{})}
	}
	convergeNode := func() workflow.NodeInput {
		return workflow.NodeInput{NodeKey: "converge", Type: workflow.NodeTypeConverge, Name: "converge",
			Config: agentCfg(t, workflow.NodeConfig{})}
	}
	fanOutWith := func(itemsField, failPolicy string) workflow.NodeInput {
		return workflow.NodeInput{NodeKey: "fanout", Type: workflow.NodeTypeFanOut, Name: "fanout",
			Config: agentCfg(t, workflow.NodeConfig{ItemsField: itemsField, FailPolicy: failPolicy})}
	}
	chain := func() []workflow.EdgeInput {
		return []workflow.EdgeInput{
			{FromNodeKey: "upstream", ToNodeKey: "fanout"},
			{FromNodeKey: "fanout", ToNodeKey: "branch"},
			{FromNodeKey: "branch", ToNodeKey: "converge"},
			{FromNodeKey: "converge", ToNodeKey: "end"},
		}
	}

	t.Run("missing_items_field", func(t *testing.T) {
		err := publish([]workflow.NodeInput{
			upstreamWith("subtasks", "array"),
			fanOutWith("", workflow.FailPolicyRework),
			agentNode("branch"), convergeNode(), endNode(),
		}, chain())
		if err == nil {
			t.Fatalf("publish without items_field: expected error, got nil")
		}
		if !strings.Contains(err.Error(), "items_field") {
			t.Fatalf("publish err = %v, want mention of items_field", err)
		}
	})

	t.Run("items_field_not_array", func(t *testing.T) {
		err := publish([]workflow.NodeInput{
			upstreamWith("subtasks", "string"),
			fanOutWith("subtasks", workflow.FailPolicyRework),
			agentNode("branch"), convergeNode(), endNode(),
		}, chain())
		if err == nil {
			t.Fatalf("publish items_field=string upstream: expected error, got nil")
		}
		if !strings.Contains(err.Error(), "array") {
			t.Fatalf("publish err = %v, want mention of 'array'", err)
		}
	})

	t.Run("illegal_fail_policy", func(t *testing.T) {
		err := publish([]workflow.NodeInput{
			upstreamWith("subtasks", "array"),
			fanOutWith("subtasks", "explode"),
			agentNode("branch"), convergeNode(), endNode(),
		}, chain())
		if err == nil {
			t.Fatalf("publish fail_policy=explode: expected error, got nil")
		}
		if !strings.Contains(err.Error(), "fail_policy") && !strings.Contains(err.Error(), "explode") {
			t.Fatalf("publish err = %v, want mention of fail_policy", err)
		}
	})
}

// ---------------------------------------------------------------------------
// AC2: publish-time validation — fan_out↔converge pairing
// ---------------------------------------------------------------------------

// TestAC2ConvergePairing covers the orphan fan_out / orphan converge
// cases from PRD AC2 / R6. Pairing is reachability-based (BFS), so a
// fan_out whose branch eventually hits a converge passes; a fan_out
// whose branch leads to end (no converge) fails.
func TestAC2ConvergePairing(t *testing.T) {
	ctx := context.Background()
	publish := func(nodes []workflow.NodeInput, edges []workflow.EdgeInput) error {
		templates := workflow.NewTemplateService(testHandler.Queries, testPool)
		detail, err := templates.CreateTemplate(ctx, workflow.CreateTemplateParams{
			WorkspaceID: util.MustParseUUID(testWorkspaceID),
			Key:         fmt.Sprintf("ac2-pair-%d", time.Now().UnixNano()),
			Name:        "ac2",
			CreatedBy:   util.MustParseUUID(testUserID),
			Nodes:       nodes, Edges: edges,
		})
		if err != nil {
			return err
		}
		_, perr := templates.PublishTemplate(ctx, util.MustParseUUID(testWorkspaceID), detail.Template.ID)
		return perr
	}
	upstreamNode := workflow.NodeInput{
		NodeKey: "upstream", Type: workflow.NodeTypeAgent, Name: "upstream",
		Config: agentCfg(t, workflow.NodeConfig{
			Role:          workflow.RoleExecutor,
			AgentSelector: "WF Placeholder Executor",
			ExitFields: &workflow.ExitFieldsSchema{Fields: []workflow.ExitFieldSpec{
				{Name: "subtasks", Type: "array"},
			}},
		}),
	}
	fanOutNode := workflow.NodeInput{NodeKey: "fanout", Type: workflow.NodeTypeFanOut, Name: "fanout",
		Config: agentCfg(t, workflow.NodeConfig{ItemsField: "subtasks"})}
	branchNode := workflow.NodeInput{NodeKey: "branch", Type: workflow.NodeTypeAgent, Name: "branch",
		Config: agentCfg(t, workflow.NodeConfig{Role: workflow.RoleExecutor, AgentSelector: "WF Placeholder Executor"})}
	convergeNode := workflow.NodeInput{NodeKey: "converge", Type: workflow.NodeTypeConverge, Name: "converge",
		Config: agentCfg(t, workflow.NodeConfig{})}
	endNode := workflow.NodeInput{NodeKey: "end", Type: workflow.NodeTypeEnd, Name: "end",
		Config: agentCfg(t, workflow.NodeConfig{})}

	t.Run("orphan_fanout_no_converge", func(t *testing.T) {
		err := publish([]workflow.NodeInput{upstreamNode, fanOutNode, branchNode, endNode},
			[]workflow.EdgeInput{
				{FromNodeKey: "upstream", ToNodeKey: "fanout"},
				{FromNodeKey: "fanout", ToNodeKey: "branch"},
				{FromNodeKey: "branch", ToNodeKey: "end"},
			})
		if err == nil {
			t.Fatalf("publish orphan fan_out: expected error, got nil")
		}
		if !strings.Contains(err.Error(), "converge") {
			t.Fatalf("err = %v, want mention of converge", err)
		}
	})

	t.Run("orphan_converge_no_fanout", func(t *testing.T) {
		err := publish([]workflow.NodeInput{upstreamNode, branchNode, convergeNode, endNode},
			[]workflow.EdgeInput{
				{FromNodeKey: "upstream", ToNodeKey: "branch"},
				{FromNodeKey: "branch", ToNodeKey: "converge"},
				{FromNodeKey: "converge", ToNodeKey: "end"},
			})
		if err == nil {
			t.Fatalf("publish orphan converge: expected error, got nil")
		}
		if !strings.Contains(err.Error(), "converge") {
			t.Fatalf("err = %v, want mention of converge", err)
		}
	})

	t.Run("paired_passes", func(t *testing.T) {
		// Sanity: the canonical fan_out → branch → converge → end shape
		// publishes cleanly. setupWorkflowAPIFixture creates the agents
		// and publishes the template in its setup; if it returns at all
		// (no Fatal), publish accepted the pairing.
		nodes, edges := fanOutE2ETemplate(t, workflow.FailPolicyRework, workflow.RoleExecutor)
		f := setupWorkflowAPIFixture(t, "ac2-paired", nodes, edges)
		if !f.templateID.Valid {
			t.Fatalf("paired publish produced no template id")
		}
	})
}

// ---------------------------------------------------------------------------
// AC3: upstream submission with subtasks → engine expands N children
// ---------------------------------------------------------------------------

func TestAC3FanOutExpandsSubtasks(t *testing.T) {
	nodes, edges := fanOutE2ETemplate(t, workflow.FailPolicyRework, workflow.RoleExecutor)
	f := setupWorkflowAPIFixture(t, "ac3-expand", nodes, edges)
	runID := uuidToString(f.run.ID)
	executorName := executorAgentName(f.executorID)
	upstreamTask := f.stepTask(t, "upstream")

	items := []any{
		validFanOutItem("child-a", executorName),
		validFanOutItem("child-b", executorName),
		validFanOutItem("child-c", executorName),
	}
	w := postFanOutSubmission(t, f.wh, upstreamTask, f.executorID, items)
	if w.Code != http.StatusCreated {
		t.Fatalf("upstream submission = %d, want 201; body=%s", w.Code, w.Body.String())
	}

	// fan_out step → passed immediately (pure splitter).
	if got := stepStatusForNodeKey(t, runID, "fanout"); got != workflow.StepPassed {
		t.Fatalf("fan_out status = %q, want passed", got)
	}
	// converge step → pending (waiting for children).
	if got := stepStatusForNodeKey(t, runID, "converge"); got != workflow.StepPending {
		t.Fatalf("converge status = %q, want pending", got)
	}
	// Three child step rows under branch, each with a child issue.
	tasks := childTasksForRun(t, runID)
	if len(tasks) != 3 {
		t.Fatalf("fan_out expanded %d children, want 3", len(tasks))
	}
	for _, taskID := range tasks {
		var issueID, parentIssueID string
		var issueStatus string
		if err := testPool.QueryRow(context.Background(), `
			SELECT si.issue_id::text, i.parent_issue_id::text, i.status
			FROM step_instance si
			JOIN issue i ON i.id = si.issue_id
			WHERE si.agent_task_id = $1
		`, taskID).Scan(&issueID, &parentIssueID, &issueStatus); err != nil {
			t.Fatalf("read child issue for task %s: %v", taskID, err)
		}
		if issueStatus != "todo" {
			t.Errorf("child issue %s status = %q, want todo", issueID, issueStatus)
		}
		if parentIssueID != uuidToString(f.run.IntakeIssueID) {
			t.Errorf("child issue %s parent = %s, want intake %s", issueID, parentIssueID, uuidToString(f.run.IntakeIssueID))
		}
	}
}

// ---------------------------------------------------------------------------
// AC4: subtask item missing required fields → activation refuses
// ---------------------------------------------------------------------------

// TestAC4SubtaskMissingFields pins the three missing-field cases from
// PRD AC4. As documented in the file header, the observable HTTP
// behavior is: the upstream submission returns 201 (its schema is
// valid — subtasks is just an array), and fan_out activation refuses
// the malformed item via failActivation → fan_out step blocked + run
// paused + initiator inbox notification.
func TestAC4SubtaskMissingFields(t *testing.T) {
	// Each case omits exactly one required field; the other two are
	// filled with a valid agent name (resolved post-fixture). The
	// parseSubtasks layer refuses the malformed item before any
	// dispatch lands.
	cases := []struct {
		name string
		// build returns the item minus the field under test; the
		// agent_selector is filled in per-fixture to keep resolver
		// happy (so the failure is isolated to the targeted field).
		build func(agentName string) map[string]any
	}{
		{"missing_title", func(a string) map[string]any {
			return map[string]any{"instructions": "no title", "agent_selector": a}
		}},
		{"missing_instructions", func(a string) map[string]any {
			return map[string]any{"title": "no instr", "agent_selector": a}
		}},
		{"missing_agent_selector", func(_ string) map[string]any {
			// agent_selector deliberately absent; pass empty string
			// to keep the closure signature uniform (it's ignored).
			return map[string]any{"title": "no agent", "instructions": "do"}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			nodes, edges := fanOutE2ETemplate(t, workflow.FailPolicyRework, workflow.RoleExecutor)
			f := setupWorkflowAPIFixture(t, "ac4-"+tc.name, nodes, edges)
			runID := uuidToString(f.run.ID)
			executorName := executorAgentName(f.executorID)
			item := tc.build(executorName)
			upstreamTask := f.stepTask(t, "upstream")

			w := postFanOutSubmission(t, f.wh, upstreamTask, f.executorID, []any{item})
			if w.Code != http.StatusCreated {
				t.Fatalf("upstream submission = %d, want 201 (schema-valid; validation is post-commit); body=%s",
					w.Code, w.Body.String())
			}

			// fan_out step → blocked; run → paused or failed.
			if got := stepStatusForNodeKey(t, runID, "fanout"); got != workflow.StepBlocked {
				t.Fatalf("fan_out status = %q, want blocked (parseSubtasks rejected %s)", got, tc.name)
			}
			if got := runStatusForRun(t, runID); got != workflow.RunPaused && got != workflow.RunFailed {
				t.Fatalf("run status = %q, want paused or failed (failActivation)", got)
			}
			// Zero children expanded (atomic rollback).
			if tasks := childTasksForRun(t, runID); len(tasks) != 0 {
				t.Fatalf("fan_out expanded %d children, want 0 (atomic rollback on parse fail)", len(tasks))
			}
			// Initiator notified.
			if n := countInboxType(t, "workflow_blocked"); n < 1 {
				t.Fatalf("workflow_blocked inbox = %d, want >=1 (initiator notified)", n)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// AC5: subtask agent_selector resolve fail → activation refuses
// ---------------------------------------------------------------------------

func TestAC5AgentSelectorResolveFail(t *testing.T) {
	nodes, edges := fanOutE2ETemplate(t, workflow.FailPolicyRework, workflow.RoleExecutor)
	f := setupWorkflowAPIFixture(t, "ac5-bad-agent", nodes, edges)
	runID := uuidToString(f.run.ID)

	items := []any{map[string]any{
		"title":          "x",
		"instructions":   "y",
		"agent_selector": "non-existent-agent-xyz",
	}}
	upstreamTask := f.stepTask(t, "upstream")
	w := postFanOutSubmission(t, f.wh, upstreamTask, f.executorID, items)
	if w.Code != http.StatusCreated {
		t.Fatalf("upstream submission = %d, want 201; body=%s", w.Code, w.Body.String())
	}

	if got := stepStatusForNodeKey(t, runID, "fanout"); got != workflow.StepBlocked {
		t.Fatalf("fan_out status = %q, want blocked (agent_selector resolve fail)", got)
	}
	if got := runStatusForRun(t, runID); got != workflow.RunPaused && got != workflow.RunFailed {
		t.Fatalf("run status = %q, want paused or failed", got)
	}
	if tasks := childTasksForRun(t, runID); len(tasks) != 0 {
		t.Fatalf("expanded %d children, want 0 (atomic rollback on agent resolve fail)", len(tasks))
	}
	if n := countInboxType(t, "workflow_blocked"); n < 1 {
		t.Fatalf("workflow_blocked inbox = %d, want >=1", n)
	}
}

// ---------------------------------------------------------------------------
// AC6: subtask labels reference unknown name → activation refuses
// ---------------------------------------------------------------------------

func TestAC6LabelsNotFound(t *testing.T) {
	nodes, edges := fanOutE2ETemplate(t, workflow.FailPolicyRework, workflow.RoleExecutor)
	f := setupWorkflowAPIFixture(t, "ac6-bad-label", nodes, edges)
	runID := uuidToString(f.run.ID)
	executorName := executorAgentName(f.executorID)

	items := []any{map[string]any{
		"title":          "x",
		"instructions":   "y",
		"agent_selector": executorName,
		"labels":         []any{"nonexistent-label-xyz"},
	}}
	upstreamTask := f.stepTask(t, "upstream")
	w := postFanOutSubmission(t, f.wh, upstreamTask, f.executorID, items)
	if w.Code != http.StatusCreated {
		t.Fatalf("upstream submission = %d, want 201; body=%s", w.Code, w.Body.String())
	}

	if got := stepStatusForNodeKey(t, runID, "fanout"); got != workflow.StepBlocked {
		t.Fatalf("fan_out status = %q, want blocked (unknown label)", got)
	}
	if tasks := childTasksForRun(t, runID); len(tasks) != 0 {
		t.Fatalf("expanded %d children, want 0 (atomic rollback on label check)", len(tasks))
	}
}

// ---------------------------------------------------------------------------
// AC7-AC9: fail_policy (fail / blocked / rework) with evaluator branch
// ---------------------------------------------------------------------------

// ac79Setup publishes a fan_out template with branch=evaluator (so the
// test can drive verdict=fail directly), starts a run, and drives the
// upstream submission to expand N children. Returns the fixture, the
// branch agent id (evaluator), and the freshly-bound branch child tasks.
//
// branch.MaxAttempts=1: the first verdict=fail lands the child
// directly in the definitive-fail branch of consumeVerdictTx
// (engine.go:503), which is where Wave 2's fail_policy dispatch
// (handleChildStepTerminal) fires. With the default max_attempts=3
// the first fail would instead take the P0 retry path
// (engine.go:491-502) and never reach the policy decision —
// defeating the test. AC9 specifically exercises reworkChildStepScope
// which only runs in the definitive-fail branch.
func ac79Setup(t *testing.T, key, failPolicy string, childCount int) (*workflowAPIFixture, string, []string) {
	t.Helper()
	nodes, edges := fanOutE2ETemplate(t, failPolicy, workflow.RoleEvaluator)
	// Force the branch node's MaxAttempts=1 so a single fail verdict
	// reaches the fail_policy decision (not the P0 retry loop).
	for i := range nodes {
		if nodes[i].NodeKey != "branch" {
			continue
		}
		cfg, err := workflow.ParseNodeConfig(nodes[i].Config)
		if err != nil {
			t.Fatalf("parse branch cfg: %v", err)
		}
		cfg.MaxAttempts = 1
		raw, _ := json.Marshal(cfg)
		nodes[i].Config = raw
	}
	f := setupWorkflowAPIFixture(t, key, nodes, edges)
	runID := uuidToString(f.run.ID)
	executorName := executorAgentName(f.executorID)

	items := make([]any, childCount)
	for i := 0; i < childCount; i++ {
		items[i] = validFanOutItem(fmt.Sprintf("c%d", i), executorName)
	}
	upstreamTask := f.stepTask(t, "upstream")
	w := postFanOutSubmission(t, f.wh, upstreamTask, f.executorID, items)
	if w.Code != http.StatusCreated {
		t.Fatalf("upstream submission = %d; body=%s", w.Code, w.Body.String())
	}
	tasks := childTasksForRun(t, runID)
	if len(tasks) != childCount {
		t.Fatalf("fan_out expanded %d children, want %d", len(tasks), childCount)
	}
	return f, f.evaluatorID, tasks
}

// TestAC7FailPolicyFail: any child fail → every other active child
// skipped, run → failed, initiator notified (PRD AC7).
func TestAC7FailPolicyFail(t *testing.T) {
	f, evaluatorID, tasks := ac79Setup(t, "ac7-fail", workflow.FailPolicyFail, 3)
	runID := uuidToString(f.run.ID)

	// Inject a fail verdict on child #0.
	w := postVerdictWithResult(t, f.wh, tasks[0], evaluatorID, workflow.VerdictFail, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("child[0] fail verdict = %d; body=%s", w.Code, w.Body.String())
	}

	// Run → failed.
	if got := runStatusForRun(t, runID); got != workflow.RunFailed {
		t.Fatalf("run = %q, want failed", got)
	}
	// Every child is terminal non-passed. The failing child itself is
	// 'failed'; the others flip to 'skipped' under policy=fail.
	statuses := childStepStatuses(t, runID)
	if len(statuses) != 3 {
		t.Fatalf("child statuses = %v, want 3 entries", statuses)
	}
	for i, s := range statuses {
		switch s {
		case workflow.StepFailed, workflow.StepSkipped:
			// ok
		default:
			t.Errorf("child[%d] status = %q, want failed or skipped (policy=fail all-stop)", i, s)
		}
	}
	// Converge step never passes.
	if got := stepStatusForNodeKey(t, runID, "converge"); got == workflow.StepPassed {
		t.Fatalf("converge = passed; want any non-passed (run failed before convergence)")
	}
}

// TestAC8FailPolicyBlocked: any child fail → run → paused, converge →
// blocked, initiator/reviewer notified (PRD AC8).
func TestAC8FailPolicyBlocked(t *testing.T) {
	f, evaluatorID, tasks := ac79Setup(t, "ac8-blocked", workflow.FailPolicyBlocked, 2)
	runID := uuidToString(f.run.ID)

	w := postVerdictWithResult(t, f.wh, tasks[0], evaluatorID, workflow.VerdictFail, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("child[0] fail verdict = %d; body=%s", w.Code, w.Body.String())
	}

	// Run → paused.
	if got := runStatusForRun(t, runID); got != workflow.RunPaused && got != workflow.RunFailed {
		t.Fatalf("run = %q, want paused (or failed if policy auto-failed)", got)
	}
	// Converge → blocked (or pending if the policy landed before the
	// pre-created converge row caught up — both are non-passed).
	if got := stepStatusForNodeKey(t, runID, "converge"); got == workflow.StepPassed {
		t.Fatalf("converge = passed; want blocked or pending (policy=blocked)")
	}
	// Reviewer/initiator notified.
	if n := countInboxType(t, "workflow_blocked"); n < 1 {
		t.Fatalf("workflow_blocked inbox = %d, want >=1 (reviewer notified)", n)
	}
}

// TestAC9FailPolicyRework: a child fail → that child gets attempt++
// (a fresh step row in its slot) while siblings stay in their
// pre-submission state (PRD AC9 / design.md §4.4).
func TestAC9FailPolicyRework(t *testing.T) {
	f, evaluatorID, tasks := ac79Setup(t, "ac9-rework", workflow.FailPolicyRework, 2)
	runID := uuidToString(f.run.ID)

	// Snapshot attempts before fail.
	beforeAttempts := childStepAttempts(t, runID)
	if len(beforeAttempts) != 2 {
		t.Fatalf("pre-fail attempts = %v, want 2", beforeAttempts)
	}

	// Inject fail on child #0 (slot 0, attempt=1).
	w := postVerdictWithResult(t, f.wh, tasks[0], evaluatorID, workflow.VerdictFail, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("child[0] fail verdict = %d; body=%s", w.Code, w.Body.String())
	}

	// After rework: child #0 has a new attempt in its slot
	// (attempt = childAttemptSlot*0 + 2), sibling unchanged.
	afterAttempts := childStepAttempts(t, runID)
	slot0Before := beforeAttempts[0]
	slot1Before := beforeAttempts[1]
	// Sibling attempt must still exist after the rework.
	siblingStillThere := false
	for _, a := range afterAttempts {
		if a == slot1Before {
			siblingStillThere = true
		}
	}
	if !siblingStillThere {
		t.Fatalf("sibling slot %d disappeared after rework; attempts=%v (BFS leak)",
			slot1Before, afterAttempts)
	}
	// Child #0 must have a strictly higher attempt in its own slot.
	reworked := false
	for _, a := range afterAttempts {
		if a > slot0Before && (int(a)/1024) == (int(slot0Before)/1024) {
			reworked = true
		}
	}
	if !reworked {
		t.Fatalf("no new attempt in child #0 slot after rework; attempts=%v", afterAttempts)
	}
}

// ---------------------------------------------------------------------------
// AC10: converge AND — concurrent child completions activate it exactly once
// ---------------------------------------------------------------------------

func TestAC10ConvergeANDConcurrent(t *testing.T) {
	nodes, edges := fanOutE2ETemplate(t, workflow.FailPolicyRework, workflow.RoleExecutor)
	f := setupWorkflowAPIFixture(t, "ac10-concurrent", nodes, edges)
	runID := uuidToString(f.run.ID)
	executorName := executorAgentName(f.executorID)

	// Three children.
	items := []any{
		validFanOutItem("c0", executorName),
		validFanOutItem("c1", executorName),
		validFanOutItem("c2", executorName),
	}
	upstreamTask := f.stepTask(t, "upstream")
	w := postFanOutSubmission(t, f.wh, upstreamTask, f.executorID, items)
	if w.Code != http.StatusCreated {
		t.Fatalf("upstream submission = %d; body=%s", w.Code, w.Body.String())
	}
	tasks := childTasksForRun(t, runID)
	if len(tasks) != 3 {
		t.Fatalf("fan_out expanded %d children, want 3", len(tasks))
	}

	// Fire DONE submissions concurrently. Each goroutine takes the
	// HTTP handler path; the engine's SELECT FOR UPDATE guarantees
	// only one converge activation lands.
	var wg sync.WaitGroup
	recs := make([]*httptest.ResponseRecorder, len(tasks))
	for i, taskID := range tasks {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			rec := httptest.NewRecorder()
			f.wh.CreateSubmission(rec, matRequest(t, "POST", "/api/tasks/"+id+"/submission", id, f.executorID, map[string]any{
				"status":      workflow.SubmissionDone,
				"exit_fields": map[string]any{},
			}))
			recs[i] = rec
		}(i, taskID)
	}
	wg.Wait()

	// Every submission must have been accepted (the run lock serializes
	// the post-commit work; the Layer-1 schema accepts an empty
	// exit_fields map for the branch executor node which declares none).
	for i, rec := range recs {
		if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
			t.Errorf("child[%d] submission = %d, want 201 or 200; body=%s",
				i, rec.Code, rec.Body.String())
		}
	}

	// Converge → passed exactly once.
	if got := stepStatusForNodeKey(t, runID, "converge"); got != workflow.StepPassed {
		t.Fatalf("converge = %q, want passed", got)
	}
	var convergePassCount int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM step_transition st
		JOIN step_instance si ON si.id = st.step_instance_id
		WHERE si.run_id = $1 AND si.node_key = 'converge'
		  AND st.to_status = 'passed'
	`, runID).Scan(&convergePassCount); err != nil {
		t.Fatalf("count converge pass transitions: %v", err)
	}
	if convergePassCount != 1 {
		t.Fatalf("converge pending→passed count = %d, want exactly 1 (concurrent activation guard)",
			convergePassCount)
	}

	// Downstream end node activated.
	if got := stepStatusForNodeKey(t, runID, "end"); got != workflow.StepActive && got != workflow.StepPassed {
		t.Fatalf("end = %q, want active or passed (post-converge activation)", got)
	}
}

// ---------------------------------------------------------------------------
// AC11: flag-off — fan_out in a template does not weaken the gate
// ---------------------------------------------------------------------------

// TestAC11FanOutFlagOff pins that adding fan_out to a published
// template does not weaken the P0 flag-off invariant (PRD AC11):
// when workflow_engine is off, the inbound hook returns 404
// (indistinguishable from an unknown token). The route-level gate is
// already covered by TestWorkflowRoutesGatedByFeatureFlag in
// cmd/server/workflow_gate_test.go and the listener no-op +
// zero-emission proofs by TestWorkflowListenerFlagOffIsNoOp /
// TestWorkflowListenerFlagOffEmitsNoWorkflowEvents in
// cmd/server/workflow_listeners_test.go — none of those tests depend
// on the template shape, so adding fan_out transitively keeps them
// green. This test is the fan_out-specific rail.
func TestAC11FanOutFlagOff(t *testing.T) {
	nodes, edges := fanOutE2ETemplate(t, workflow.FailPolicyRework, workflow.RoleExecutor)
	f := setupHookFixture(t, "ac11-flagoff", nodes, edges)

	// Rebuild the hook handler with the flag OFF.
	provider := featureflag.NewStaticProvider()
	provider.Set(workflow.FlagEngine, featureflag.Rule{Default: false})
	engine := workflow.NewEngine(testHandler.Queries, testPool, testHandler.IssueService, testHandler.TaskService, events.New())
	hh := NewWorkflowHookHandler(testHandler.Queries, engine, featureflag.NewService(provider), nil, nil, nil)

	// A valid token + valid payload still 404s under flag-off — the
	// hook handler checks the flag before revealing whether the token
	// exists (workflow_hook.go:162).
	w := httptest.NewRecorder()
	hh.HandleInboundHook(w, hookRequest(t, f.token, map[string]any{
		"title":     "AC11 flag-off push",
		"source_id": fmt.Sprintf("ac11-src-%d", time.Now().UnixNano()),
	}))
	if w.Code != http.StatusNotFound {
		t.Fatalf("flag-off hook = %d, want 404 (AC11); body=%s", w.Code, w.Body.String())
	}

	// No run created.
	var runs int
	if err := testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM workflow_run WHERE workspace_id = $1 AND template_id = $2`,
		testWorkspaceID, f.templateID).Scan(&runs); err != nil {
		t.Fatalf("count runs: %v", err)
	}
	if runs != 0 {
		t.Fatalf("flag-off created %d runs, want 0 (listener no-op)", runs)
	}
}

// ---------------------------------------------------------------------------
// AC12: P0 regression — fan_out engine changes leave the linear path intact
// ---------------------------------------------------------------------------

// TestAC12P0Regression re-runs a representative slice of the P0
// linear-chain e2e (work → gate → review → end) to prove the DAG
// engine adaptations (NextAfterAll returns a 1-element slice for
// linear templates) leave P0 behavior byte-identical. The full P0
// AC1-AC9 suite lives in workflow_e2e_ac_test.go and runs as part of
// `make check`; this test is the smoke-rail that runs alongside the
// AC1-AC11 fan_out tests when -run '^TestAC' filters to this file.
func TestAC12P0Regression(t *testing.T) {
	nodes, edges := e2eChainTemplate(t) // work → gate → review(acceptance) → end
	f := setupWorkflowAPIFixture(t, "ac12-p0-regression", nodes, edges)
	wh := f.wh
	wrh := runHandlerFor(f)
	runID := uuidToString(f.run.ID)

	postSubmission(t, wh, f.stepTask(t, "work"), f.executorID, "DONE",
		map[string]any{"pr_url": "https://example/pr/ac12"})
	postVerdict(t, wh, f.stepTask(t, "gate"), f.evaluatorID, nil)
	if got := runStatusForRun(t, runID); got != workflow.RunWaitingAcceptance {
		t.Fatalf("run = %q, want waiting_acceptance (P0 chain intact)", got)
	}
	approveRunAcceptance(t, wrh, runID)
	if got := runStatusForRun(t, runID); got != workflow.RunCompleted {
		t.Fatalf("run = %q, want completed", got)
	}
	for _, node := range []string{"work", "gate", "review", "end"} {
		if got := stepStatusForRun(t, runID, node); got != workflow.StepPassed {
			t.Fatalf("P0 step %q = %q, want passed", node, got)
		}
	}
}

// ---------------------------------------------------------------------------
// AC13: upstream submission omits items_field → 422 (P0 Layer-1 schema)
// ---------------------------------------------------------------------------

// TestAC13MissingItemsField pins the case where the upstream
// submission entirely omits the items_field the fan_out expects.
// Because fanOutE2ETemplate marks the upstream "subtasks" field as
// Required=true, the P0 Layer-1 schema validator refuses the
// submission with a structured 422 before the engine ever runs.
// (design.md §5 row 1; Wave 2's engine-level fallback handles the
// case where the field is declared but not required — covered by
// internal/workflow/fanout_test.go TestActivateFanOutNode_MissingItemsField.)
func TestAC13MissingItemsField(t *testing.T) {
	nodes, edges := fanOutE2ETemplate(t, workflow.FailPolicyRework, workflow.RoleExecutor)
	f := setupWorkflowAPIFixture(t, "ac13-missing-items", nodes, edges)
	taskID := f.stepTask(t, "upstream")
	runID := uuidToString(f.run.ID)

	w := httptest.NewRecorder()
	f.wh.CreateSubmission(w, matRequest(t, "POST", "/api/tasks/"+taskID+"/submission", taskID, f.executorID, map[string]any{
		"status":      workflow.SubmissionDone,
		"exit_fields": map[string]any{}, // missing "subtasks"
	}))
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing-items submission = %d, want 422; body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Fields []struct {
			Name string `json:"name"`
			Code string `json:"code"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode 422 body: %v", err)
	}
	if len(body.Fields) != 1 || body.Fields[0].Name != "subtasks" || body.Fields[0].Code != "missing" {
		t.Fatalf("structured fields = %+v, want one missing subtasks", body.Fields)
	}
	// No submission row written, no verdict derived, upstream untouched.
	var subs int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM submission s
		JOIN step_instance si ON si.id = s.step_instance_id
		WHERE si.run_id = $1
	`, f.run.ID).Scan(&subs); err != nil {
		t.Fatalf("count submissions: %v", err)
	}
	if subs != 0 {
		t.Fatalf("submission rows = %d, want 0 (rejected before write)", subs)
	}
	if got := stepStatusForNodeKey(t, runID, "upstream"); got != workflow.StepActive {
		t.Fatalf("upstream step = %q, want still active", got)
	}
}

// _ = db.New keeps the db import meaningful if a future edit drops
// every direct call site — the test pool is db.New(testPool) at heart.
var _ = db.New
