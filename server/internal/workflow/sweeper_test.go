package workflow

// sweeper_test.go — P1-5 Sweeper AC coverage (PRD AC1–AC6). The fixture
// (main_test.go) gives each test an isolated workspace with a real
// runtime-backed agent so activateNode's full dispatch path
// (issue create + EnqueueTaskForIssueWithHandoff + dispatch link) is
// exercised end-to-end. Tests drive sweepOnce directly rather than Run
// (no tick waiting); AC1 (periodic scan) is covered by Run's ticker
// construction at the type level.

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// newSweeper builds a Sweeper wired to the fixture's engine with a
// millisecond tick + second-grade blocked timeout so the tests never
// block on real time. flag=nil means "always on" for behavioral tests;
// flag-off tests pass their own callback.
func newSweeper(f *testFixture, flag func(string) bool) *Sweeper {
	return NewSweeper(f.engine, flag).
		WithInterval(50 * time.Millisecond).
		WithBlockedTimeout(100 * time.Millisecond)
}

// clearTask simulates the "active step lost its task" inconsistency by
// nulling agent_task_id (and issue_id, so activateNode creates fresh).
func clearTask(f *testFixture, step db.StepInstance) {
	f.t.Helper()
	if _, err := f.pool.Exec(context.Background(),
		`UPDATE step_instance SET agent_task_id = NULL, issue_id = NULL, updated_at = now() WHERE id = $1`,
		step.ID); err != nil {
		f.t.Fatalf("clear task: %v", err)
	}
}

// setRunningWithExpiredDeadline promotes an active step to 'running' with
// a deadline just past — simulating the post-dispatch state where the
// engine eventually populates deadline_at (P2 feature; the sweeper
// handles it TODAY).
func setRunningWithExpiredDeadline(f *testFixture, step db.StepInstance) {
	f.t.Helper()
	if _, err := f.pool.Exec(context.Background(),
		`UPDATE step_instance
		 SET status = 'running',
		     deadline_at = now() - interval '1 minute',
		     updated_at = now()
		 WHERE id = $1`, step.ID); err != nil {
		f.t.Fatalf("set running+deadline: %v", err)
	}
}

// forceBlockedAndResume puts a step into the "blocked but run is running"
// inconsistent state the sweeper reconciles: mark the step blocked with
// an old started_at, then force the run back to running (which the
// engine proper would never leave uncorrected). startedAge is how old
// the step's started_at should read; the test sweeps with a much smaller
// blockedTimeout so the age always trips.
func forceBlockedAndResume(f *testFixture, step db.StepInstance, runID pgtype.UUID, startedAge time.Duration) {
	f.t.Helper()
	// Pass the interval as a Postgres literal string — pgx cannot encode
	// Go numeric types directly into the interval OID.
	intervalStr := strconv.FormatInt(int64(startedAge.Seconds()), 10) + " seconds"
	if _, err := f.pool.Exec(context.Background(),
		`UPDATE step_instance
		 SET status = 'blocked',
		     started_at = now() - $1::interval,
		     finished_at = now() - $1::interval,
		     updated_at = now()
		 WHERE id = $2`, intervalStr, step.ID); err != nil {
		f.t.Fatalf("force blocked: %v", err)
	}
	if _, err := f.pool.Exec(context.Background(),
		`UPDATE workflow_run SET status = 'running', updated_at = now() WHERE id = $1`,
		runID); err != nil {
		f.t.Fatalf("resume run: %v", err)
	}
}

// AC2: active step with no agent_task_id → re-dispatch writes one back.
func TestSweeper_ActiveStepNoTask(t *testing.T) {
	f := newTestFixture(t)
	tmpl := f.createPublishedTemplate("sweep-notask", []NodeInput{
		agentNode("work", RoleExecutor, "Executor Agent", NodeConfig{}),
		typedNode("end", NodeTypeEnd, NodeConfig{}),
	}, linearEdges("work", "end"))
	run := f.startRun(tmpl, "sweep-notask-1", "Sweep AC2")

	work := f.latestStep(run.ID, "work")
	if !work.AgentTaskID.Valid {
		t.Fatalf("precondition: plan step should have a task after StartRun")
	}
	originalTaskID := work.AgentTaskID
	clearTask(f, work)

	s := newSweeper(f, nil)
	s.sweepOnce(context.Background())

	// Give activateNode's post-commit side effects (which run inline in
	// tests — no daemon wakeup channel to delay through) a beat, then
	// re-read. The sweep is synchronous so this is effectively immediate.
	after := f.latestStep(run.ID, "work")
	if !after.AgentTaskID.Valid {
		t.Fatalf("after sweep: work step still has no task")
	}
	if after.AgentTaskID == originalTaskID {
		t.Fatalf("after sweep: task id was not refreshed (still %v)", originalTaskID)
	}
	if after.Status != StepActive {
		t.Fatalf("after sweep: status = %q, want active", after.Status)
	}
	if got := f.runStatus(run.ID); got != RunRunning {
		t.Fatalf("run = %q, want running", got)
	}
}

// AC3: running step with expired deadline_at → reset to active with a
// fresh task.
func TestSweeper_RunningStepDeadlineExpired(t *testing.T) {
	f := newTestFixture(t)
	tmpl := f.createPublishedTemplate("sweep-deadline", []NodeInput{
		agentNode("work", RoleExecutor, "Executor Agent", NodeConfig{}),
		typedNode("end", NodeTypeEnd, NodeConfig{}),
	}, linearEdges("work", "end"))
	run := f.startRun(tmpl, "sweep-deadline-1", "Sweep AC3")

	work := f.latestStep(run.ID, "work")
	originalTaskID := work.AgentTaskID
	setRunningWithExpiredDeadline(f, work)

	s := newSweeper(f, nil)
	s.sweepOnce(context.Background())

	after := f.latestStep(run.ID, "work")
	if after.Status != StepActive {
		t.Fatalf("after sweep: status = %q, want active (reset + re-activate)", after.Status)
	}
	if !after.AgentTaskID.Valid {
		t.Fatalf("after sweep: missing fresh task")
	}
	if after.AgentTaskID == originalTaskID {
		t.Fatalf("after sweep: task id was not refreshed (still %v)", originalTaskID)
	}
	if got := f.runStatus(run.ID); got != RunRunning {
		t.Fatalf("run = %q, want running", got)
	}
}

// AC4: blocked step older than timeout → run paused + inbox row.
func TestSweeper_BlockedStepTimeout(t *testing.T) {
	f := newTestFixture(t)
	tmpl := f.createPublishedTemplate("sweep-blocked", []NodeInput{
		agentNode("work", RoleExecutor, "Executor Agent", NodeConfig{}),
		typedNode("end", NodeTypeEnd, NodeConfig{}),
	}, linearEdges("work", "end"))
	run := f.startRun(tmpl, "sweep-blocked-1", "Sweep AC4")

	work := f.latestStep(run.ID, "work")
	forceBlockedAndResume(f, work, run.ID, 1*time.Hour)

	before := f.inboxCount("workflow_blocked")
	s := newSweeper(f, nil)
	s.sweepOnce(context.Background())

	if got := f.runStatus(run.ID); got != RunPaused {
		t.Fatalf("run = %q, want paused", got)
	}
	if after := f.inboxCount("workflow_blocked"); after != before+1 {
		t.Fatalf("inbox workflow_blocked = %d (before %d), want +1", after, before)
	}
	after := f.latestStep(run.ID, "work")
	if after.Status != StepBlocked {
		t.Fatalf("blocked step status changed to %q; want left blocked", after.Status)
	}
}

// AC5: flag-off workspace → sweeper no-op (the candidate row is skipped
// by the per-workspace flag check, not by the SQL filter — the SQL is
// flag-agnostic; the skip happens in sweepOnce so a single process
// serving mixed flag-state workspaces honors each one correctly).
func TestSweeper_FlagOffNoOp(t *testing.T) {
	f := newTestFixture(t)
	tmpl := f.createPublishedTemplate("sweep-flagoff", []NodeInput{
		agentNode("work", RoleExecutor, "Executor Agent", NodeConfig{}),
		typedNode("end", NodeTypeEnd, NodeConfig{}),
	}, linearEdges("work", "end"))
	run := f.startRun(tmpl, "sweep-flagoff-1", "Sweep AC5")

	work := f.latestStep(run.ID, "work")
	clearTask(f, work)

	beforeInbox := f.inboxCount("workflow_blocked")
	s := newSweeper(f, func(workspaceID string) bool {
		return false // flag off for every workspace
	})
	s.sweepOnce(context.Background())

	after := f.latestStep(run.ID, "work")
	if after.AgentTaskID.Valid {
		t.Fatalf("flag off: sweeper wrote a task (%v); want no-op", after.AgentTaskID)
	}
	if got := f.runStatus(run.ID); got != RunRunning {
		t.Fatalf("flag off: run status flipped to %q; want running (no-op)", got)
	}
	if got := f.inboxCount("workflow_blocked"); got != beforeInbox {
		t.Fatalf("flag off: inbox count changed (%d → %d); want no-op", beforeInbox, got)
	}
}

// AC1 wiring proof: Run exits cleanly when ctx cancels and ticker
// construction does not panic. The tick itself is covered by the
// sweepOnce tests above (Run is just ticker → sweepOnce).
func TestSweeper_RunStopsOnContextCancel(t *testing.T) {
	f := newTestFixture(t)
	s := newSweeper(f, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("Run returned %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Run did not exit within 2s of ctx cancel")
	}
}

// Boundary self-check (quality-guidelines.md "DAG 边界 Self-Check
// Pattern"): the sweeper must never write agent_task_queue directly.
// This grep turns the rule from a comment contract into a mechanical
// assertion. Permitted: explanatory mentions in Go comments (lines
// beginning with //) and the boundary rule's own description. Forbidden:
// any UPDATE/DELETE/INSERT INTO agent_task_queue statement actually
// issued against the DB (which would bypass service layer event/
// activity/WS side effects).
func TestSweeper_DoesNotTouchAgentTaskQueueDirectly(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(wd, "sweeper.go"))
	if err != nil {
		t.Fatalf("read sweeper.go: %v", err)
	}
	lines := strings.Split(string(b), "\n")
	forbidden := []string{
		"UPDATE agent_task_queue",
		"DELETE FROM agent_task_queue",
		"INSERT INTO agent_task_queue",
		"agent_task_queue SET",
	}
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		// Skip Go comments — they may quote the forbidden pattern as
		// part of explaining the boundary rule itself.
		if strings.HasPrefix(trim, "//") || strings.HasPrefix(trim, "/*") || strings.HasPrefix(trim, "*") {
			continue
		}
		for _, pat := range forbidden {
			if strings.Contains(line, pat) {
				t.Fatalf("sweeper.go:%d contains forbidden agent_task_queue write %q (use service.TaskService instead)",
					i+1, pat)
			}
		}
	}
	// Sanity: the file is non-empty and still routes dispatch through
	// Engine.activateNode (otherwise the boundary grep above would
	// silently pass on a file that does nothing).
	if !strings.Contains(string(b), "s.engine.activateNode") {
		t.Fatalf("sweeper.go does not route dispatch through Engine.activateNode (boundary grep is meaningless)")
	}
}

// TestSweeper_P0Regression is a placeholder name — the regression run is
// the standard `go test ./internal/workflow/... ./internal/handler/`
// command in the task's verification step. The success of that command
// IS the AC6 evidence; this stub documents the linkage.
func TestSweeper_P0RegressionDocOnly(t *testing.T) {
	// AC6 is satisfied by the full suite passing alongside this test
	// (engine_test.go / engine_more_test.go / fanout_test.go /
	// converge_test.go / gate_test.go / seed_test.go /
	// template_fanout_test.go / rework_test.go). No inline assertion
	// needed: a regression in any of those fails the test run.
	t.Log("AC6 P0/P1 regression: see `go test ./internal/workflow/... ./internal/handler/` output")
}

// TestSweeper_SkipsRunNotRunning is the race guard: by the time the
// sweeper re-reads the run, it may have been paused/cancelled. The
// sweepOnce classification must skip those, not error out.
func TestSweeper_SkipsRunNotRunning(t *testing.T) {
	f := newTestFixture(t)
	tmpl := f.createPublishedTemplate("sweep-race", []NodeInput{
		agentNode("work", RoleExecutor, "Executor Agent", NodeConfig{}),
		typedNode("end", NodeTypeEnd, NodeConfig{}),
	}, linearEdges("work", "end"))
	run := f.startRun(tmpl, "sweep-race-1", "Sweep race")

	work := f.latestStep(run.ID, "work")
	clearTask(f, work)
	// Race: pause the run AFTER the SELECT would have happened but BEFORE
	// the sweeper's re-read. We simulate this by pausing now and then
	// sweeping — sweepOnce's SELECT will still find the row (status=
	// active + task_id NULL), but loadSweepTarget refuses to act on a
	// non-running run.
	if _, err := f.pool.Exec(context.Background(),
		`UPDATE workflow_run SET status = 'paused', updated_at = now() WHERE id = $1`,
		run.ID); err != nil {
		t.Fatalf("pause run: %v", err)
	}

	s := newSweeper(f, nil)
	s.sweepOnce(context.Background()) // must not crash, must not re-dispatch

	after := f.latestStep(run.ID, "work")
	if after.AgentTaskID.Valid {
		t.Fatalf("sweeper re-dispatched into a non-running run; want no-op")
	}
}
