package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/workflow"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/featureflag"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// workflow_listeners_test.go — Wave 2 coverage for the bus → engine wiring:
// task events published on the bus drive step transitions through the
// registered listeners, gated per-workspace by the workflow_engine flag.
// Engine-side mapping semantics (retry budget, guard races) live in
// internal/workflow/task_events_test.go; here we assert the listener layer:
// flag gating, task-id extraction, and event → engine reachability.
//
// Uses the integration TestMain fixture (testPool / testWorkspaceID /
// testUserID) and the shared "Integration Test Agent".

// workflowListenerFixture bundles a published work→end template, a started
// run, and the bus the listeners are registered on.
type workflowListenerFixture struct {
	engine     *workflow.Engine
	bus        *events.Bus
	templateID pgtype.UUID
	run        db.WorkflowRun
}

// setupWorkflowListenerFixture publishes a work(executor)→end template bound
// to the shared integration agent, starts a run (which enqueues the work
// step's task), and registers the listeners with the given flag state.
func setupWorkflowListenerFixture(t *testing.T, flagOn bool, maxAttempts int32) *workflowListenerFixture {
	t.Helper()
	ctx := context.Background()

	queries := db.New(testPool)
	bus := events.New()
	tasks := service.NewTaskService(queries, testPool, nil, bus)
	issues := service.NewIssueService(queries, testPool, bus, nil, tasks)
	engine := workflow.NewEngine(queries, testPool, issues, tasks, bus)

	workCfg, err := json.Marshal(workflow.NodeConfig{
		Role:          workflow.RoleExecutor,
		AgentSelector: "Integration Test Agent",
		MaxAttempts:   maxAttempts,
	})
	if err != nil {
		t.Fatalf("marshal node config: %v", err)
	}
	templates := workflow.NewTemplateService(queries, testPool)
	detail, err := templates.CreateTemplate(ctx, workflow.CreateTemplateParams{
		WorkspaceID: util.MustParseUUID(testWorkspaceID),
		Key:         fmt.Sprintf("listener-%d", time.Now().UnixNano()),
		Name:        "listener test",
		CreatedBy:   util.MustParseUUID(testUserID),
		Nodes: []workflow.NodeInput{
			{NodeKey: "work", Type: workflow.NodeTypeAgent, Name: "work", Config: workCfg},
			{NodeKey: "end", Type: workflow.NodeTypeEnd, Name: "end", Config: nil},
		},
		Edges: []workflow.EdgeInput{{FromNodeKey: "work", ToNodeKey: "end"}},
	})
	if err != nil {
		t.Fatalf("create template: %v", err)
	}
	published, err := templates.PublishTemplate(ctx, util.MustParseUUID(testWorkspaceID), detail.Template.ID)
	if err != nil {
		t.Fatalf("publish template: %v", err)
	}

	provider := featureflag.NewStaticProvider()
	provider.Set(workflow.FlagEngine, featureflag.Rule{Default: flagOn})
	registerWorkflowListeners(bus, engine, featureflag.NewService(provider))

	run, created, err := engine.StartRun(ctx, workflow.StartRunParams{
		WorkspaceID: util.MustParseUUID(testWorkspaceID),
		TemplateID:  published.Template.ID,
		SourceType:  "hook",
		SourceID:    fmt.Sprintf("listener-src-%d", time.Now().UnixNano()),
		Title:       "listener test run",
		InitiatorID: util.MustParseUUID(testUserID),
	})
	if err != nil || !created {
		t.Fatalf("start run: %v (created=%v)", err, created)
	}

	f := &workflowListenerFixture{engine: engine, bus: bus, templateID: published.Template.ID, run: run}
	t.Cleanup(func() {
		ctx := context.Background()
		// Tasks before issues (agent_task_queue.issue_id FK), issues before
		// runs (workflow_run.template_id is RESTRICT, intake FK SET NULL).
		testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE issue_id IN (SELECT id FROM issue WHERE parent_issue_id = $1)`, run.IntakeIssueID)
		testPool.Exec(ctx, `DELETE FROM issue WHERE parent_issue_id = $1`, run.IntakeIssueID)
		testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, run.IntakeIssueID)
		testPool.Exec(ctx, `DELETE FROM workflow_run WHERE id = $1`, run.ID)
		testPool.Exec(ctx, `DELETE FROM workflow_template WHERE id = $1`, f.templateID)
		testPool.Exec(ctx, `DELETE FROM inbox_item WHERE workspace_id = $1 AND type LIKE 'workflow_%'`, testWorkspaceID)
	})
	return f
}

// stepByNode reads the latest step for the run's work node.
func (f *workflowListenerFixture) workStep(t *testing.T) db.StepInstance {
	t.Helper()
	step, err := db.New(testPool).GetLatestStepInstanceForNode(context.Background(), db.GetLatestStepInstanceForNodeParams{
		RunID: f.run.ID, NodeKey: "work",
	})
	if err != nil {
		t.Fatalf("latest work step: %v", err)
	}
	return step
}

func (f *workflowListenerFixture) runStatus(t *testing.T) string {
	t.Helper()
	run, err := db.New(testPool).GetWorkflowRun(context.Background(), f.run.ID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	return run.Status
}

func (f *workflowListenerFixture) inboxCount(t *testing.T, typ string) int {
	t.Helper()
	var n int
	if err := testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM inbox_item WHERE workspace_id = $1 AND type = $2`, testWorkspaceID, typ).Scan(&n); err != nil {
		t.Fatalf("count inbox: %v", err)
	}
	return n
}

// publishTaskEvent emits one daemon task lifecycle event on the fixture bus,
// shaped like service.broadcastTaskEvent's payload.
func (f *workflowListenerFixture) publishTaskEvent(eventType string, taskID pgtype.UUID) {
	f.bus.Publish(events.Event{
		Type:        eventType,
		WorkspaceID: testWorkspaceID,
		ActorType:   "system",
		Payload: map[string]any{
			"task_id": util.UUIDToString(taskID),
		},
	})
}

func TestWorkflowListenerTaskDispatch(t *testing.T) {
	f := setupWorkflowListenerFixture(t, true, 3)
	step := f.workStep(t)
	f.publishTaskEvent(protocol.EventTaskDispatch, step.AgentTaskID)

	if got := f.workStep(t); got.Status != workflow.StepDispatched {
		t.Fatalf("step = %q, want dispatched", got.Status)
	}
}

func TestWorkflowListenerTaskFailedRetries(t *testing.T) {
	f := setupWorkflowListenerFixture(t, true, 3)
	step := f.workStep(t)
	f.publishTaskEvent(protocol.EventTaskFailed, step.AgentTaskID)

	retry := f.workStep(t)
	if retry.Attempt != 2 || retry.Status != workflow.StepActive {
		t.Fatalf("retry = (attempt %d, %q), want (2, active)", retry.Attempt, retry.Status)
	}
	if got := f.runStatus(t); got != workflow.RunRunning {
		t.Fatalf("run = %q, want running", got)
	}
}

func TestWorkflowListenerTaskFailedEscalates(t *testing.T) {
	f := setupWorkflowListenerFixture(t, true, 1)
	step := f.workStep(t)
	f.publishTaskEvent(protocol.EventTaskFailed, step.AgentTaskID)

	if got := f.workStep(t); got.Status != workflow.StepFailed {
		t.Fatalf("step = %q, want failed", got.Status)
	}
	if got := f.runStatus(t); got != workflow.RunPaused {
		t.Fatalf("run = %q, want paused", got)
	}
	if n := f.inboxCount(t, "workflow_escalated"); n != 1 {
		t.Fatalf("workflow_escalated inbox = %d, want 1", n)
	}
}

func TestWorkflowListenerTaskCompletedWithoutSubmission(t *testing.T) {
	f := setupWorkflowListenerFixture(t, true, 3)
	step := f.workStep(t)
	f.publishTaskEvent(protocol.EventTaskCompleted, step.AgentTaskID)

	if got := f.workStep(t); got.Status != workflow.StepBlocked {
		t.Fatalf("step = %q, want blocked", got.Status)
	}
	if got := f.runStatus(t); got != workflow.RunPaused {
		t.Fatalf("run = %q, want paused", got)
	}
	if n := f.inboxCount(t, "workflow_blocked"); n != 1 {
		t.Fatalf("workflow_blocked inbox = %d, want 1", n)
	}
}

func TestWorkflowListenerFlagOffIsNoOp(t *testing.T) {
	f := setupWorkflowListenerFixture(t, false, 3)
	step := f.workStep(t)
	for _, eventType := range []string{protocol.EventTaskDispatch, protocol.EventTaskFailed, protocol.EventTaskCompleted} {
		f.publishTaskEvent(eventType, step.AgentTaskID)
	}
	if got := f.workStep(t); got.Status != workflow.StepActive {
		t.Fatalf("step = %q, want active (flag off == never subscribed, AC6)", got.Status)
	}
	if got := f.runStatus(t); got != workflow.RunRunning {
		t.Fatalf("run = %q, want running", got)
	}
}

// TestWorkflowListenerFlagOffEmitsNoWorkflowEvents is the AC6 zero-emission
// proof: with the flag off the listeners never reach the engine, so the bus
// carries no workflow:* events at all — the WS fanout has nothing new to
// broadcast and flag-off deployments are byte-identical at the event layer.
func TestWorkflowListenerFlagOffEmitsNoWorkflowEvents(t *testing.T) {
	f := setupWorkflowListenerFixture(t, false, 3)

	var mu sync.Mutex
	var workflowEvents []string
	f.bus.SubscribeAll(func(e events.Event) {
		if strings.HasPrefix(e.Type, "workflow:") {
			mu.Lock()
			workflowEvents = append(workflowEvents, e.Type)
			mu.Unlock()
		}
	})

	step := f.workStep(t)
	for _, eventType := range []string{protocol.EventTaskDispatch, protocol.EventTaskFailed, protocol.EventTaskCompleted} {
		f.publishTaskEvent(eventType, step.AgentTaskID)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(workflowEvents) != 0 {
		t.Fatalf("workflow events with flag off = %v, want none (AC6)", workflowEvents)
	}
}

func TestWorkflowListenerIgnoresNonWorkflowTask(t *testing.T) {
	f := setupWorkflowListenerFixture(t, true, 3)
	// A task id bound to no step must flow through the listener as a quiet
	// no-op (the common case for every non-workflow task in the system).
	f.publishTaskEvent(protocol.EventTaskCompleted, pgtype.UUID{Valid: true})
	f.publishTaskEvent(protocol.EventTaskFailed, pgtype.UUID{Valid: true})
	if got := f.workStep(t); got.Status != workflow.StepActive {
		t.Fatalf("step = %q, want active", got.Status)
	}
	// Malformed payloads must not panic the bus dispatch either.
	f.bus.Publish(events.Event{Type: protocol.EventTaskCompleted, WorkspaceID: testWorkspaceID, Payload: map[string]any{"task_id": 42}})
	f.bus.Publish(events.Event{Type: protocol.EventTaskFailed, WorkspaceID: testWorkspaceID, Payload: "not-a-map"})
	if got := f.workStep(t); got.Status != workflow.StepActive {
		t.Fatalf("step = %q, want active after malformed events", got.Status)
	}
}
