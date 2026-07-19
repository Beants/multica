package workflow

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
)

// events_test.go — 3.5a coverage: every committed run/step status change
// publishes its workspace-scoped WS event (events.go emission rule). The bus
// is synchronous (internal/events.Bus.Publish dispatches inline), so a
// collector subscribed before an engine call observes every event the call
// emitted by the time it returns — no sleeps, no races.

// workflowEvent is one collected workflow:* event with its decoded payload.
type workflowEvent struct {
	Type           string
	RunID          string
	StepInstanceID string
	Status         string
}

// eventCollector taps the fixture bus for workflow:* events.
type eventCollector struct {
	mu     sync.Mutex
	events []workflowEvent
}

func newEventCollector(bus *events.Bus) *eventCollector {
	c := &eventCollector{}
	bus.SubscribeAll(func(e events.Event) {
		if !strings.HasPrefix(e.Type, "workflow:") {
			return
		}
		we := workflowEvent{Type: e.Type}
		if payload, ok := e.Payload.(map[string]any); ok {
			we.RunID, _ = payload["run_id"].(string)
			we.StepInstanceID, _ = payload["step_instance_id"].(string)
			we.Status, _ = payload["status"].(string)
		}
		c.mu.Lock()
		c.events = append(c.events, we)
		c.mu.Unlock()
	})
	return c
}

// reset drops everything collected so far, scoping assertions to the engine
// calls that follow.
func (c *eventCollector) reset() {
	c.mu.Lock()
	c.events = nil
	c.mu.Unlock()
}

// requireEvent finds one event matching every non-empty criterion, failing
// the test with the full collected log when absent.
func (c *eventCollector) requireEvent(t *testing.T, typ, status, stepID string) workflowEvent {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.events {
		if e.Type != typ {
			continue
		}
		if status != "" && e.Status != status {
			continue
		}
		if stepID != "" && e.StepInstanceID != stepID {
			continue
		}
		return e
	}
	t.Fatalf("missing event type=%q status=%q step=%q; collected: %v", typ, status, stepID, c.events)
	return workflowEvent{}
}

// countRunEvents returns the run-updated statuses in emission order.
func (c *eventCollector) runEventStatuses() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, e := range c.events {
		if e.Type == EventRunUpdated {
			out = append(out, e.Status)
		}
	}
	return out
}

// requireSubsequence asserts the run-updated statuses contain want as an
// in-order subsequence (extra events in between are fine).
func requireRunEventSubsequence(t *testing.T, got, want []string) {
	t.Helper()
	i := 0
	for _, s := range got {
		if i < len(want) && s == want[i] {
			i++
		}
	}
	if i != len(want) {
		t.Fatalf("run events %v do not contain subsequence %v", got, want)
	}
}

func TestStartRunEmitsRunAndStepEvents(t *testing.T) {
	f := newTestFixture(t)
	collector := newEventCollector(f.bus)
	tmpl := chainTemplate(f, "chain")
	run := f.startRun(tmpl, "ext-ev-start", "Event start")

	plan := f.latestStep(run.ID, "plan")
	e := collector.requireEvent(t, EventRunUpdated, RunRunning, "")
	if e.RunID != util.UUIDToString(run.ID) {
		t.Fatalf("run event run_id = %q, want %q", e.RunID, util.UUIDToString(run.ID))
	}
	e = collector.requireEvent(t, EventStepUpdated, StepActive, util.UUIDToString(plan.ID))
	if e.RunID != util.UUIDToString(run.ID) {
		t.Fatalf("step event run_id = %q, want %q", e.RunID, util.UUIDToString(run.ID))
	}
}

func TestVerdictAdvanceEmitsStepEvents(t *testing.T) {
	f := newTestFixture(t)
	collector := newEventCollector(f.bus)
	tmpl := chainTemplate(f, "chain")
	run := f.startRun(tmpl, "ext-ev-advance", "Event advance")
	collector.reset()

	f.passExecutorStep(run.ID, "plan", map[string]any{"spec_url": "https://spec"})

	plan := f.latestStep(run.ID, "plan")
	implement := f.latestStep(run.ID, "implement")
	collector.requireEvent(t, EventStepUpdated, StepPassed, util.UUIDToString(plan.ID))
	collector.requireEvent(t, EventStepUpdated, StepActive, util.UUIDToString(implement.ID))
	// A mid-chain pass must not touch run status.
	if statuses := collector.runEventStatuses(); len(statuses) != 0 {
		t.Fatalf("run events on mid-chain advance = %v, want none", statuses)
	}
}

func TestBlockedVerdictEmitsStepAndRunPaused(t *testing.T) {
	f := newTestFixture(t)
	collector := newEventCollector(f.bus)
	tmpl := chainTemplate(f, "chain")
	run := f.startRun(tmpl, "ext-ev-blocked", "Event blocked")
	collector.reset()

	step := f.latestStep(run.ID, "plan")
	if _, _, err := f.engine.RecordSubmission(context.Background(), step.AgentTaskID, SubmissionInput{
		Status: SubmissionBlocked, RawSummary: "cannot proceed",
	}); err != nil {
		t.Fatalf("record blocked submission: %v", err)
	}

	collector.requireEvent(t, EventStepUpdated, StepBlocked, util.UUIDToString(step.ID))
	collector.requireEvent(t, EventRunUpdated, RunPaused, "")
}

func TestAcceptanceFlowEmitsRunStatusSequence(t *testing.T) {
	f := newTestFixture(t)
	collector := newEventCollector(f.bus)
	tmpl := chainTemplate(f, "chain")
	run := f.startRun(tmpl, "ext-ev-accept", "Event acceptance")

	acc := driveToAcceptance(f, run)
	collector.requireEvent(t, EventRunUpdated, RunWaitingAcceptance, "")
	acceptStep := f.latestStep(run.ID, "accept")
	collector.requireEvent(t, EventStepUpdated, StepActive, util.UUIDToString(acceptStep.ID))

	collector.reset()
	if err := f.engine.ApproveAcceptance(context.Background(), run.ID, acc.ID, util.MustParseUUID(f.memberID)); err != nil {
		t.Fatalf("approve: %v", err)
	}
	// Approve passes the acceptance step, activates+passes the end node, and
	// completes the run (chainTemplate's accept is the final acceptance).
	collector.requireEvent(t, EventStepUpdated, StepPassed, util.UUIDToString(acceptStep.ID))
	endStep := f.latestStep(run.ID, "end")
	collector.requireEvent(t, EventStepUpdated, StepPassed, util.UUIDToString(endStep.ID))
	requireRunEventSubsequence(t, collector.runEventStatuses(),
		[]string{RunRunning, RunCompleted})
}

func TestRejectEmitsReworkInvalidationEvents(t *testing.T) {
	f := newTestFixture(t)
	collector := newEventCollector(f.bus)
	tmpl := chainTemplate(f, "chain")
	run := f.startRun(tmpl, "ext-ev-reject", "Event reject")
	acc := driveToAcceptance(f, run)
	implementOld := f.latestStep(run.ID, "implement")
	reviewStep := f.latestStep(run.ID, "review")
	acceptStep := f.latestStep(run.ID, "accept")
	endStep := f.latestStep(run.ID, "end")
	collector.reset()

	if err := f.engine.RejectAcceptance(context.Background(), run.ID, acc.ID, util.MustParseUUID(f.memberID), "implement", "wrong API shape"); err != nil {
		t.Fatalf("reject: %v", err)
	}

	// Run resumed, target's old attempt marked rework, every downstream
	// non-skipped step (review passed, accept active, end pending) skipped,
	// and the fresh attempt active.
	collector.requireEvent(t, EventRunUpdated, RunRunning, "")
	collector.requireEvent(t, EventStepUpdated, StepRework, util.UUIDToString(implementOld.ID))
	collector.requireEvent(t, EventStepUpdated, StepSkipped, util.UUIDToString(reviewStep.ID))
	collector.requireEvent(t, EventStepUpdated, StepSkipped, util.UUIDToString(acceptStep.ID))
	collector.requireEvent(t, EventStepUpdated, StepSkipped, util.UUIDToString(endStep.ID))
	implementNew := f.latestStep(run.ID, "implement")
	if implementNew.ID == implementOld.ID {
		t.Fatalf("expected a fresh attempt row for implement")
	}
	collector.requireEvent(t, EventStepUpdated, StepActive, util.UUIDToString(implementNew.ID))
}

func TestRunCompletionEmitsCompleted(t *testing.T) {
	f := newTestFixture(t)
	collector := newEventCollector(f.bus)
	tmpl := f.createPublishedTemplate("quick", []NodeInput{
		agentNode("work", RoleExecutor, "Executor Agent", NodeConfig{}),
		typedNode("end", NodeTypeEnd, NodeConfig{}),
	}, linearEdges("work", "end"))
	run := f.startRun(tmpl, "ext-ev-complete", "Event complete")
	collector.reset()

	f.passExecutorStep(run.ID, "work", nil)

	collector.requireEvent(t, EventRunUpdated, RunCompleted, "")
	work := f.latestStep(run.ID, "work")
	collector.requireEvent(t, EventStepUpdated, StepPassed, util.UUIDToString(work.ID))
}

func TestTaskEventMappingsEmitStepEvents(t *testing.T) {
	f := newTestFixture(t)
	collector := newEventCollector(f.bus)
	tmpl := f.createPublishedTemplate("taskev", []NodeInput{
		agentNode("work", RoleExecutor, "Executor Agent", NodeConfig{}),
		typedNode("end", NodeTypeEnd, NodeConfig{}),
	}, linearEdges("work", "end"))
	run := f.startRun(tmpl, "ext-ev-task", "Event task")
	ctx := context.Background()

	// active → dispatched.
	step := f.latestStep(run.ID, "work")
	collector.reset()
	if err := f.engine.HandleTaskDispatch(ctx, step.AgentTaskID); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	collector.requireEvent(t, EventStepUpdated, StepDispatched, util.UUIDToString(step.ID))

	// task:failed with retry budget left → failed + fresh attempt active.
	collector.reset()
	if err := f.engine.HandleTaskFailed(ctx, step.AgentTaskID); err != nil {
		t.Fatalf("failed: %v", err)
	}
	collector.requireEvent(t, EventStepUpdated, StepFailed, util.UUIDToString(step.ID))
	retry := f.latestStep(run.ID, "work")
	if retry.ID == step.ID {
		t.Fatalf("expected a fresh attempt row")
	}
	collector.requireEvent(t, EventStepUpdated, StepActive, util.UUIDToString(retry.ID))

	// task:completed with no submission → blocked + run paused.
	collector.reset()
	if err := f.engine.HandleTaskCompleted(ctx, retry.AgentTaskID); err != nil {
		t.Fatalf("completed: %v", err)
	}
	collector.requireEvent(t, EventStepUpdated, StepBlocked, util.UUIDToString(retry.ID))
	collector.requireEvent(t, EventRunUpdated, RunPaused, "")
}

// TestNilBusEngineSkipsEmission pins the nil-bus guard: an engine built
// without a bus (the flag-off deployment never reaches engine code, but the
// guard keeps direct engine use safe) runs the full happy path without
// panicking.
func TestNilBusEngineSkipsEmission(t *testing.T) {
	f := newTestFixture(t)
	f.engine = NewEngine(f.queries, f.pool, f.issues, f.tasks, nil)
	tmpl := f.createPublishedTemplate("nilbus", []NodeInput{
		agentNode("work", RoleExecutor, "Executor Agent", NodeConfig{}),
		typedNode("end", NodeTypeEnd, NodeConfig{}),
	}, linearEdges("work", "end"))
	run := f.startRun(tmpl, "ext-ev-nilbus", "Nil bus")
	f.passExecutorStep(run.ID, "work", nil)
	if got := f.runStatus(run.ID); got != RunCompleted {
		t.Fatalf("run status = %q, want completed (state machine unaffected by nil bus)", got)
	}
}
