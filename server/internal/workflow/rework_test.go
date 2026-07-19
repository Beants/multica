package workflow

import (
	"context"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// rework_test.go — targeted rework semantics (design.md §4.4): downstream
// invalidation including already-passed steps, rework_context injection,
// and both circuit breakers. Plus the §4.2 injection-hygiene unit tests.

// driveToAcceptance walks the chain template to the acceptance wait. Safe
// across rework rounds: nodes whose latest step is already passed (upstream
// of the rework target) are left alone.
func driveToAcceptance(f *testFixture, run db.WorkflowRun) db.Acceptance {
	f.t.Helper()
	for _, key := range []string{"plan", "implement"} {
		if f.latestStep(run.ID, key).Status == StepActive {
			exit := map[string]any(nil)
			if key == "plan" {
				exit = map[string]any{"spec_url": "https://spec"}
			}
			f.passExecutorStep(run.ID, key, exit)
		}
	}
	if review := f.latestStep(run.ID, "review"); review.Status == StepActive {
		if _, err := f.engine.RecordVerdict(context.Background(), review.AgentTaskID, VerdictInput{Result: VerdictPass}); err != nil {
			f.t.Fatalf("record verdict: %v", err)
		}
	}
	acc, err := f.queries.GetPendingAcceptanceByRun(context.Background(), run.ID)
	if err != nil {
		f.t.Fatalf("pending acceptance: %v", err)
	}
	return acc
}

func TestReworkInvalidatesDownstreamAndReenters(t *testing.T) {
	f := newTestFixture(t)
	tmpl := chainTemplate(f, "chain")
	run := f.startRun(tmpl, "ext-rework", "Rework")
	acc := driveToAcceptance(f, run)

	if err := f.engine.RejectAcceptance(context.Background(), run.ID, acc.ID, util.MustParseUUID(f.memberID), "implement", "wrong API shape"); err != nil {
		t.Fatalf("reject: %v", err)
	}

	if got := f.runStatus(run.ID); got != RunRunning {
		t.Fatalf("run = %q, want running after reject", got)
	}
	// Target re-entered with a fresh attempt, old attempt marked rework.
	impl := f.latestStep(run.ID, "implement")
	if impl.Status != StepActive || impl.Attempt != 2 {
		t.Fatalf("implement = %q attempt %d, want active attempt 2", impl.Status, impl.Attempt)
	}
	// AC2: the already-PASSED downstream review step is invalidated to
	// skipped (it must re-run), and so are the acceptance step and the
	// pre-created end row.
	steps, err := f.queries.ListStepInstancesForRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("list steps: %v", err)
	}
	latestByNode := map[string]db.StepInstance{}
	for _, s := range steps {
		if cur, ok := latestByNode[s.NodeKey]; !ok || s.Attempt > cur.Attempt {
			latestByNode[s.NodeKey] = s
		}
	}
	if got := latestByNode["review"].Status; got != StepSkipped {
		t.Fatalf("review = %q, want skipped (downstream invalidation incl. passed)", got)
	}
	if got := latestByNode["accept"].Status; got != StepSkipped {
		t.Fatalf("accept = %q, want skipped", got)
	}
	if got := latestByNode["end"].Status; got != StepSkipped {
		t.Fatalf("end = %q, want skipped", got)
	}
	// The invalidation wrote step_transition rows (AC4 traceability).
	reviewTransitions := f.transitionsForStep(latestByNode["review"].ID)
	if n := len(reviewTransitions); n == 0 || reviewTransitions[n-1][1] != StepSkipped {
		t.Fatalf("review transitions = %v, want a →skipped entry", reviewTransitions)
	}
	// The rejected acceptance row carries the rework context.
	decided, err := f.queries.GetAcceptance(context.Background(), acc.ID)
	if err != nil {
		t.Fatalf("get acceptance: %v", err)
	}
	if decided.Status != "rejected" || decided.RejectToNodeKey.String != "implement" {
		t.Fatalf("acceptance = %q to %q", decided.Status, decided.RejectToNodeKey.String)
	}
	if !strings.Contains(string(decided.ReworkContext), "wrong API shape") {
		t.Fatalf("rework context = %s, want reason recorded", decided.ReworkContext)
	}

	// Rework context reaches the new attempt's handoff note (D-8).
	var note string
	if err := f.pool.QueryRow(context.Background(), `
		SELECT coalesce(handoff_note, '') FROM agent_task_queue
		WHERE issue_id = (SELECT issue_id FROM step_instance WHERE id = $1)
	`, impl.ID).Scan(&note); err != nil {
		t.Fatalf("read handoff note: %v", err)
	}
	if !strings.Contains(note, "[rework]") || !strings.Contains(note, "wrong API shape") {
		t.Fatalf("handoff note missing rework context:\n%s", note)
	}

	// Re-running to acceptance re-executes the invalidated gates (AC2: the
	// flow passes review AGAIN before reaching acceptance).
	f.passExecutorStep(run.ID, "implement", nil)
	review := f.latestStep(run.ID, "review")
	if review.Status != StepActive || review.Attempt != 2 {
		t.Fatalf("review = %q attempt %d, want active attempt 2 (re-run after invalidation)", review.Status, review.Attempt)
	}
	if _, err := f.engine.RecordVerdict(context.Background(), review.AgentTaskID, VerdictInput{Result: VerdictPass}); err != nil {
		t.Fatalf("record verdict: %v", err)
	}
	if got := f.runStatus(run.ID); got != RunWaitingAcceptance {
		t.Fatalf("run = %q, want waiting_acceptance (reached acceptance again)", got)
	}
	if _, err := f.queries.GetPendingAcceptanceByRun(context.Background(), run.ID); err != nil {
		t.Fatalf("new pending acceptance missing: %v", err)
	}
}

func TestReworkRejectsActiveTarget(t *testing.T) {
	f := newTestFixture(t)
	tmpl := chainTemplate(f, "chain")
	run := f.startRun(tmpl, "ext-active-target", "Active target")

	// plan is in-flight: rework into it must be refused (blueprint §8.1).
	err := f.engine.RequestRework(context.Background(), run.ID, "plan", nil)
	if err != ErrReworkTargetActive {
		t.Fatalf("rework into active step = %v, want ErrReworkTargetActive", err)
	}
	if err := f.engine.RequestRework(context.Background(), run.ID, "nonexistent", nil); err != ErrReworkTargetUnknown {
		t.Fatalf("rework into unknown node = %v, want ErrReworkTargetUnknown", err)
	}
}

func TestRejectionCircuitBreaker(t *testing.T) {
	f := newTestFixture(t)
	tmpl := chainTemplate(f, "chain")
	run := f.startRun(tmpl, "ext-breaker2", "Breaker two")

	// Breaker ②: three acceptance rejections in one run pause it, even
	// though the rework target passes between rounds (which resets the
	// consecutive counter ① — review #3).
	for round := 1; round <= 3; round++ {
		acc := driveToAcceptance(f, run)
		if err := f.engine.RejectAcceptance(context.Background(), run.ID, acc.ID, util.MustParseUUID(f.memberID), "implement", "not good enough"); err != nil {
			t.Fatalf("reject round %d: %v", round, err)
		}
		if round < 3 {
			if got := f.runStatus(run.ID); got != RunRunning {
				t.Fatalf("round %d: run = %q, want running", round, got)
			}
		}
	}
	if got := f.runStatus(run.ID); got != RunPaused {
		t.Fatalf("run = %q, want paused after 3 rejections", got)
	}
	intake, err := f.queries.GetIssue(context.Background(), run.IntakeIssueID)
	if err != nil {
		t.Fatalf("get intake: %v", err)
	}
	if intake.AssigneeType.String != "member" || intake.AssigneeID != util.MustParseUUID(f.userID) {
		t.Fatalf("intake assignee = %s/%v, want human handoff", intake.AssigneeType.String, intake.AssigneeID)
	}
	if n := f.inboxCount("workflow_circuit_breaker"); n != 1 {
		t.Fatalf("breaker inbox = %d, want 1", n)
	}
}

func TestConsecutiveReworkCircuitBreaker(t *testing.T) {
	f := newTestFixture(t)
	// Template whose rework target is an EVALUATOR node, so the test can
	// park it (verdict blocked) between rework rounds without passing it —
	// stacking consecutive reworks on the same node (breaker ①).
	tmpl := f.createPublishedTemplate("breaker1", []NodeInput{
		agentNode("work", RoleExecutor, "Executor Agent", NodeConfig{}),
		agentNode("gate", RoleEvaluator, "Evaluator Agent", NodeConfig{}),
		typedNode("accept", NodeTypeAcceptance, NodeConfig{}),
		typedNode("end", NodeTypeEnd, NodeConfig{}),
	}, linearEdges("work", "gate", "accept", "end"))
	run := f.startRun(tmpl, "ext-breaker1", "Breaker one")

	f.passExecutorStep(run.ID, "work", nil)
	gate := f.latestStep(run.ID, "gate")
	if _, err := f.engine.RecordVerdict(context.Background(), gate.AgentTaskID, VerdictInput{Result: VerdictPass}); err != nil {
		t.Fatalf("record verdict: %v", err)
	}
	acc, err := f.queries.GetPendingAcceptanceByRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("pending acceptance: %v", err)
	}

	// Round 1: rejection reworks the gate (count 1). Then park the new
	// attempt with a blocked verdict and unpause the run manually, so the
	// next RequestRework finds a terminal-but-unpassed target.
	if err := f.engine.RejectAcceptance(context.Background(), run.ID, acc.ID, util.MustParseUUID(f.memberID), "gate", "redo the gate"); err != nil {
		t.Fatalf("reject: %v", err)
	}
	for round := 2; round <= 3; round++ {
		gate = f.latestStep(run.ID, "gate")
		if gate.Status != StepActive || gate.Attempt != int32(round) {
			t.Fatalf("round %d: gate = %q attempt %d", round, gate.Status, gate.Attempt)
		}
		if _, err := f.engine.RecordVerdict(context.Background(), gate.AgentTaskID, VerdictInput{
			Result: VerdictBlocked, RootCause: "stuck",
		}); err != nil {
			t.Fatalf("round %d verdict: %v", round, err)
		}
		if got := f.runStatus(run.ID); got != RunPaused {
			t.Fatalf("round %d: run = %q, want paused (blocked)", round, got)
		}
		// A human un-pauses the run (P0 has no resume API; the test drives
		// the guarded query the resume path would use).
		if _, err := f.queries.UpdateWorkflowRunStatus(context.Background(), db.UpdateWorkflowRunStatusParams{
			NewStatus: RunRunning, ID: run.ID, ExpectedStatus: RunPaused,
		}); err != nil {
			t.Fatalf("round %d unpause: %v", round, err)
		}
		if err := f.engine.RequestRework(context.Background(), run.ID, "gate", nil); err != nil {
			t.Fatalf("round %d rework: %v", round, err)
		}
	}

	// The third consecutive rework trips breaker ①: paused + handoff.
	if got := f.runStatus(run.ID); got != RunPaused {
		t.Fatalf("run = %q, want paused after 3 consecutive reworks", got)
	}
	gate = f.latestStep(run.ID, "gate")
	if gate.Status != StepRework {
		t.Fatalf("gate = %q, want rework (no 4th attempt activated)", gate.Status)
	}
	if n := f.inboxCount("workflow_circuit_breaker"); n != 1 {
		t.Fatalf("breaker inbox = %d, want 1", n)
	}
	intake, err := f.queries.GetIssue(context.Background(), run.IntakeIssueID)
	if err != nil {
		t.Fatalf("get intake: %v", err)
	}
	if intake.AssigneeType.String != "member" {
		t.Fatalf("intake assignee = %s, want member (human handoff)", intake.AssigneeType.String)
	}
	// The consecutive-rework counter agrees with the observed history.
	count, err := f.queries.CountConsecutiveReworksForNode(context.Background(), db.CountConsecutiveReworksForNodeParams{
		RunID: run.ID, NodeKey: "gate",
	})
	if err != nil {
		t.Fatalf("count reworks: %v", err)
	}
	if count != 3 {
		t.Fatalf("consecutive reworks = %d, want 3", count)
	}
}

// ---------------------------------------------------------------------------
// §4.2 injection hygiene (pure unit tests, no DB)
// ---------------------------------------------------------------------------

func TestSanitizePromptText(t *testing.T) {
	t.Parallel()
	tests := []struct{ in, want string }{
		{"hello world", "hello world"},
		{"line one\nline two", "line one line two"},
		{"tabs\tand\r\nCRLF", "tabs and CRLF"},
		{"control\x00\x07\x1bchars", "controlchars"},
		{"  padded  spaces  ", "padded spaces"},
		{"中文 标题 保留", "中文 标题 保留"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := sanitizePromptText(tt.in); got != tt.want {
			t.Errorf("sanitizePromptText(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestFinalizeHandoffNotePrefixesEveryLine(t *testing.T) {
	t.Parallel()
	note := finalizeHandoffNote("first\nsecond\nthird")
	lines := strings.Split(note, "\n")
	if len(lines) != 3 {
		t.Fatalf("lines = %d, want 3", len(lines))
	}
	for _, l := range lines {
		if !strings.HasPrefix(l, "> ") {
			t.Fatalf("line %q missing quote prefix", l)
		}
	}
}

func TestFinalizeHandoffNoteTruncatesAt4KB(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	for i := 0; i < 200; i++ {
		b.WriteString("some fairly long line of context that the node carried\n")
	}
	note := finalizeHandoffNote(b.String())
	if len(note) > handoffNoteMaxBytes {
		t.Fatalf("note = %d bytes, want ≤ %d", len(note), handoffNoteMaxBytes)
	}
	if !strings.Contains(note, "truncated") || !strings.Contains(note, "multica step context") {
		t.Fatalf("truncated note must point at the full-context CLI read:\n%s", note[len(note)-200:])
	}
	// Truncation cut at a line boundary — every surviving line is prefixed.
	for _, l := range strings.Split(note, "\n") {
		if !strings.HasPrefix(l, "> ") {
			t.Fatalf("line %q missing quote prefix after truncation", l)
		}
	}
}

func TestRenderReworkContextSanitizes(t *testing.T) {
	t.Parallel()
	rc := &ReworkContext{
		Reason: "bad\noutput\x00here",
		History: []ReworkVerdictEntry{
			{Attempt: 1, Result: "fail", RootCause: "missing\ntests", By: "agent"},
			{Attempt: 2, Result: "fail", By: "agent"},
		},
	}
	got := renderReworkContext(rc)
	if strings.ContainsAny(got, "\n\r\x00") {
		t.Fatalf("rework context not single-line sanitized: %q", got)
	}
	if !strings.Contains(got, "bad outputhere") || !strings.Contains(got, "attempt 1: fail (missing tests)") {
		t.Fatalf("rework context = %q", got)
	}
}
