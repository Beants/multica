package workflow

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// task_events_test.go — Wave 2 coverage for the daemon task lifecycle →
// engine mapping (design.md §4.4): task:dispatch → dispatched; task:failed →
// failed + retry/escalate; task:completed without submission → blocked +
// paused + inbox. Uses the shared DB fixture (main_test.go).

// stepTaskID extracts the bound agent task id of a step or fails the test.
func stepTaskID(t *testing.T, step db.StepInstance) pgtype.UUID {
	t.Helper()
	if !step.AgentTaskID.Valid {
		t.Fatalf("step %q has no bound task", step.NodeKey)
	}
	return step.AgentTaskID
}

func TestHandleTaskDispatchMarksDispatched(t *testing.T) {
	f := newTestFixture(t)
	tmpl := f.createPublishedTemplate("task-event-dispatch", []NodeInput{
		agentNode("work", RoleExecutor, "Executor Agent", NodeConfig{}),
		typedNode("end", NodeTypeEnd, NodeConfig{}),
	}, linearEdges("work", "end"))
	run := f.startRun(tmpl, "src-dispatch", "dispatch run")
	step := f.latestStep(run.ID, "work")
	if step.Status != StepActive {
		t.Fatalf("step = %q, want active before dispatch", step.Status)
	}

	if err := f.engine.HandleTaskDispatch(context.Background(), stepTaskID(t, step)); err != nil {
		t.Fatalf("HandleTaskDispatch: %v", err)
	}
	got := f.latestStep(run.ID, "work")
	if got.Status != StepDispatched {
		t.Fatalf("step = %q, want dispatched", got.Status)
	}
	// The guarded transition wrote its step_transition row (design.md §4.6).
	found := false
	for _, pair := range f.transitionsForStep(step.ID) {
		if pair[0] == StepActive && pair[1] == StepDispatched {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing active→dispatched transition: %v", f.transitionsForStep(step.ID))
	}

	// A duplicate dispatch event is a no-op (no second transition, no error).
	if err := f.engine.HandleTaskDispatch(context.Background(), stepTaskID(t, step)); err != nil {
		t.Fatalf("duplicate HandleTaskDispatch: %v", err)
	}
	if n := len(f.transitionsForStep(step.ID)); n != 2 { // none→active (activation) + active→dispatched (event)
		t.Fatalf("transitions = %d, want 2 after duplicate dispatch", n)
	}
}

func TestHandleTaskEventsIgnoreUnknownTask(t *testing.T) {
	f := newTestFixture(t)
	unknown := pgtype.UUID{Valid: true} // zero UUID: bound to nothing
	for name, fn := range map[string]func(context.Context, pgtype.UUID) error{
		"dispatch":  f.engine.HandleTaskDispatch,
		"failed":    f.engine.HandleTaskFailed,
		"completed": f.engine.HandleTaskCompleted,
	} {
		if err := fn(context.Background(), unknown); err != nil {
			t.Fatalf("%s on unknown task: %v", name, err)
		}
	}
}

func TestHandleTaskFailedRetriesWithinBudget(t *testing.T) {
	f := newTestFixture(t)
	tmpl := f.createPublishedTemplate("task-event-retry", []NodeInput{
		agentNode("work", RoleExecutor, "Executor Agent", NodeConfig{}), // default max_attempts = 3
		typedNode("end", NodeTypeEnd, NodeConfig{}),
	}, linearEdges("work", "end"))
	run := f.startRun(tmpl, "src-retry", "retry run")
	step := f.latestStep(run.ID, "work")

	if err := f.engine.HandleTaskFailed(context.Background(), stepTaskID(t, step)); err != nil {
		t.Fatalf("HandleTaskFailed: %v", err)
	}

	if got := f.runStatus(run.ID); got != RunRunning {
		t.Fatalf("run = %q, want running (retry keeps the run alive)", got)
	}
	failed := f.transitionsForStep(step.ID)
	assertHasTransition(t, failed, StepActive, StepFailed)

	attempt2 := f.latestStep(run.ID, "work")
	if attempt2.Attempt != 2 || attempt2.Status != StepActive {
		t.Fatalf("attempt2 = (attempt %d, %q), want (2, active)", attempt2.Attempt, attempt2.Status)
	}
	// The retry dispatched a fresh task for the new attempt.
	if !attempt2.AgentTaskID.Valid {
		t.Fatalf("attempt2 has no task enqueued")
	}
	if attempt2.AgentTaskID == step.AgentTaskID {
		t.Fatalf("attempt2 reused the failed task id")
	}
}

func TestHandleTaskFailedEscalatesAtMaxAttempts(t *testing.T) {
	f := newTestFixture(t)
	tmpl := f.createPublishedTemplate("task-event-escalate", []NodeInput{
		agentNode("work", RoleExecutor, "Executor Agent", NodeConfig{MaxAttempts: 1}),
		typedNode("end", NodeTypeEnd, NodeConfig{}),
	}, linearEdges("work", "end"))
	run := f.startRun(tmpl, "src-escalate", "escalate run")
	step := f.latestStep(run.ID, "work")

	if err := f.engine.HandleTaskFailed(context.Background(), stepTaskID(t, step)); err != nil {
		t.Fatalf("HandleTaskFailed: %v", err)
	}

	if got := f.latestStep(run.ID, "work"); got.Status != StepFailed {
		t.Fatalf("step = %q, want failed", got.Status)
	}
	if got := f.runStatus(run.ID); got != RunPaused {
		t.Fatalf("run = %q, want paused (budget exhausted)", got)
	}
	if n := f.inboxCount("workflow_escalated"); n != 1 {
		t.Fatalf("workflow_escalated inbox = %d, want 1", n)
	}
	// The intake issue was handed back to the human initiator.
	var assignee string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT assignee_id::text FROM issue WHERE id = $1`, run.IntakeIssueID).Scan(&assignee); err != nil {
		t.Fatalf("read intake assignee: %v", err)
	}
	if assignee != f.userID {
		t.Fatalf("intake assignee = %q, want initiator %q", assignee, f.userID)
	}
}

func TestHandleTaskCompletedWithoutSubmissionBlocks(t *testing.T) {
	f := newTestFixture(t)
	tmpl := f.createPublishedTemplate("task-event-nosub", []NodeInput{
		agentNode("work", RoleExecutor, "Executor Agent", NodeConfig{}),
		typedNode("end", NodeTypeEnd, NodeConfig{}),
	}, linearEdges("work", "end"))
	run := f.startRun(tmpl, "src-nosub", "no-submission run")
	step := f.latestStep(run.ID, "work")

	if err := f.engine.HandleTaskCompleted(context.Background(), stepTaskID(t, step)); err != nil {
		t.Fatalf("HandleTaskCompleted: %v", err)
	}

	if got := f.latestStep(run.ID, "work"); got.Status != StepBlocked {
		t.Fatalf("step = %q, want blocked", got.Status)
	}
	if got := f.runStatus(run.ID); got != RunPaused {
		t.Fatalf("run = %q, want paused", got)
	}
	if n := f.inboxCount("workflow_blocked"); n != 1 {
		t.Fatalf("workflow_blocked inbox = %d, want 1", n)
	}
	assertHasTransition(t, f.transitionsForStep(step.ID), StepActive, StepBlocked)
}

func TestHandleTaskCompletedWithSubmissionIsNoOp(t *testing.T) {
	f := newTestFixture(t)
	tmpl := f.createPublishedTemplate("task-event-withsub", []NodeInput{
		agentNode("work", RoleExecutor, "Executor Agent", NodeConfig{}),
		agentNode("gate", RoleEvaluator, "Evaluator Agent", NodeConfig{}),
		typedNode("end", NodeTypeEnd, NodeConfig{}),
	}, linearEdges("work", "gate", "end"))
	run := f.startRun(tmpl, "src-withsub", "with-submission run")
	f.passExecutorStep(run.ID, "work", nil)

	// The evaluator step now carries a submission but no verdict yet — the
	// non-terminal middle state the completion event must not block.
	gate := f.latestStep(run.ID, "gate")
	if _, created, err := f.engine.RecordSubmission(context.Background(), stepTaskID(t, gate), SubmissionInput{
		Status: SubmissionDone,
	}); err != nil || !created {
		t.Fatalf("gate submission: created=%v err=%v", created, err)
	}
	if err := f.engine.HandleTaskCompleted(context.Background(), stepTaskID(t, gate)); err != nil {
		t.Fatalf("HandleTaskCompleted: %v", err)
	}
	if got := f.latestStep(run.ID, "gate"); got.Status != StepActive {
		t.Fatalf("gate = %q, want active (submission owns advancement)", got.Status)
	}
	if got := f.runStatus(run.ID); got != RunRunning {
		t.Fatalf("run = %q, want running", got)
	}

	// A late completion event for the already-passed work step is a no-op too.
	work := f.latestStep(run.ID, "work")
	if err := f.engine.HandleTaskCompleted(context.Background(), stepTaskID(t, work)); err != nil {
		t.Fatalf("HandleTaskCompleted on terminal step: %v", err)
	}
	if got := f.latestStep(run.ID, "work"); got.Status != StepPassed {
		t.Fatalf("work = %q, want passed", got.Status)
	}
}

func TestHandleTaskFailedOnTerminalStepIsNoOp(t *testing.T) {
	f := newTestFixture(t)
	tmpl := f.createPublishedTemplate("task-event-termfail", []NodeInput{
		agentNode("work", RoleExecutor, "Executor Agent", NodeConfig{}),
		agentNode("gate", RoleEvaluator, "Evaluator Agent", NodeConfig{}),
		typedNode("end", NodeTypeEnd, NodeConfig{}),
	}, linearEdges("work", "gate", "end"))
	run := f.startRun(tmpl, "src-termfail", "terminal-fail run")
	f.passExecutorStep(run.ID, "work", nil)

	work := f.latestStep(run.ID, "work")
	if err := f.engine.HandleTaskFailed(context.Background(), stepTaskID(t, work)); err != nil {
		t.Fatalf("HandleTaskFailed on passed step: %v", err)
	}
	if got := f.latestStep(run.ID, "work"); got.Status != StepPassed {
		t.Fatalf("work = %q, want passed (late failure ignored)", got.Status)
	}
	if got := f.runStatus(run.ID); got != RunRunning {
		t.Fatalf("run = %q, want running", got)
	}
}

// assertHasTransition fails the test when the (from,to) pair is absent.
func assertHasTransition(t *testing.T, pairs [][2]string, from, to string) {
	t.Helper()
	for _, p := range pairs {
		if p[0] == from && p[1] == to {
			return
		}
	}
	t.Fatalf("missing %s→%s transition in %v", from, to, pairs)
}
