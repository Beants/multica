package workflow

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// engine_test.go — Wave 1 state-machine coverage (design.md §6): advance,
// retry, escalate, blocked, idempotent re-signal, guarded updates, the
// NULLS NOT DISTINCT duplicate guard, rework + downstream invalidation,
// both circuit breakers, and the acceptance lifecycle.

// chainTemplate builds the 5-node reference chain:
// plan(executor) → implement(executor) → review(evaluator) → accept → end.
func chainTemplate(f *testFixture, key string) *TemplateDetail {
	return f.createPublishedTemplate(key, []NodeInput{
		agentNode("plan", RoleExecutor, "Executor Agent", NodeConfig{
			Instructions: "Plan the work",
			ExitFields:   &ExitFieldsSchema{Fields: []ExitFieldSpec{{Name: "spec_url", Type: "string", Required: true}}},
		}),
		agentNode("implement", RoleExecutor, "Executor Agent", NodeConfig{Instructions: "Build it"}),
		agentNode("review", RoleEvaluator, "Evaluator Agent", NodeConfig{Instructions: "Judge it"}),
		typedNode("accept", NodeTypeAcceptance, NodeConfig{}),
		typedNode("end", NodeTypeEnd, NodeConfig{}),
	}, linearEdges("plan", "implement", "review", "accept", "end"))
}

func TestStartRunActivatesFirstNodeAndPreCreatesNext(t *testing.T) {
	f := newTestFixture(t)
	tmpl := chainTemplate(f, "chain")
	run := f.startRun(tmpl, "ext-1", "First requirement")

	if got := f.runStatus(run.ID); got != RunRunning {
		t.Fatalf("run status = %q, want running", got)
	}
	if !run.IntakeIssueID.Valid {
		t.Fatalf("intake issue not linked")
	}
	intake, err := f.queries.GetIssue(context.Background(), run.IntakeIssueID)
	if err != nil {
		t.Fatalf("get intake: %v", err)
	}
	if intake.Title != "First requirement" || intake.Status != "todo" {
		t.Fatalf("intake = %q/%q, want First requirement/todo", intake.Title, intake.Status)
	}

	// First node: active, dispatched artifacts linked, child issue todo.
	plan := f.latestStep(run.ID, "plan")
	if plan.Status != StepActive {
		t.Fatalf("plan status = %q, want active", plan.Status)
	}
	if !plan.AgentTaskID.Valid || !plan.IssueID.Valid {
		t.Fatalf("plan step missing dispatch artifacts: task=%v issue=%v", plan.AgentTaskID, plan.IssueID)
	}
	child, err := f.queries.GetIssue(context.Background(), plan.IssueID)
	if err != nil {
		t.Fatalf("get child issue: %v", err)
	}
	wantTitle := "1-plan-attempt1"
	if child.Title != wantTitle {
		t.Fatalf("child title = %q, want %q (intake number-node-attempt)", child.Title, wantTitle)
	}
	if child.Status != "todo" {
		t.Fatalf("child status = %q, want todo (post handoff flip)", child.Status)
	}
	if child.AssigneeID != util.MustParseUUID(f.executorID) {
		t.Fatalf("child assignee = %v, want executor agent", child.AssigneeID)
	}
	// Activation order proof: exactly one task exists for the child — the
	// backlog-first create suppressed the auto-enqueue, so the explicit
	// handoff enqueue is the only dispatch (no double-dispatch).
	if n := f.countTasksForIssue(child.ID); n != 1 {
		t.Fatalf("tasks on child = %d, want exactly 1 (no double dispatch)", n)
	}
	// The handoff note carries the node context into the opening prompt.
	var note string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT coalesce(handoff_note, '') FROM agent_task_queue WHERE issue_id = $1`, child.ID).Scan(&note); err != nil {
		t.Fatalf("read handoff note: %v", err)
	}
	for _, want := range []string{"[workflow node] plan (plan)", "[instructions] Plan the work", "[exit_fields schema]", "spec_url"} {
		if !strings.Contains(note, want) {
			t.Fatalf("handoff note missing %q:\n%s", want, note)
		}
	}
	for _, line := range strings.Split(note, "\n") {
		if !strings.HasPrefix(line, "> ") {
			t.Fatalf("handoff note line not quote-prefixed: %q", line)
		}
	}

	// Second node: pre-created pending, no dispatch yet.
	impl := f.latestStep(run.ID, "implement")
	if impl.Status != StepPending || impl.AgentTaskID.Valid {
		t.Fatalf("implement = %q task=%v, want pending without task", impl.Status, impl.AgentTaskID)
	}
	if got := f.transitionsForStep(impl.ID); len(got) != 1 || got[0] != [2]string{"none", StepPending} {
		t.Fatalf("implement transitions = %v, want [none→pending]", got)
	}
}

func TestStartRunIdempotentBySource(t *testing.T) {
	f := newTestFixture(t)
	tmpl := chainTemplate(f, "chain")
	run := f.startRun(tmpl, "ext-dup", "Requirement")

	again, created, err := f.engine.StartRun(context.Background(), StartRunParams{
		WorkspaceID: util.MustParseUUID(f.workspaceID),
		TemplateID:  tmpl.Template.ID,
		SourceType:  "hook",
		SourceID:    "ext-dup",
		Title:       "Requirement (re-push)",
		InitiatorID: util.MustParseUUID(f.userID),
	})
	if err != nil {
		t.Fatalf("second StartRun: %v", err)
	}
	if created {
		t.Fatalf("re-push created a new run — idempotency broken")
	}
	if again.ID != run.ID {
		t.Fatalf("re-push returned run %v, want existing %v", again.ID, run.ID)
	}
	// Only one intake issue exists.
	var n int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM issue WHERE workspace_id = $1`, f.workspaceID).Scan(&n); err != nil {
		t.Fatalf("count issues: %v", err)
	}
	// 1 intake + 1 plan child.
	if n != 2 {
		t.Fatalf("issues = %d, want 2 (no duplicate intake)", n)
	}
}

func TestAdvanceThroughChainToAcceptance(t *testing.T) {
	f := newTestFixture(t)
	tmpl := chainTemplate(f, "chain")
	run := f.startRun(tmpl, "ext-flow", "Flow")

	f.passExecutorStep(run.ID, "plan", map[string]any{"spec_url": "https://spec"})

	plan := f.latestStep(run.ID, "plan")
	if plan.Status != StepPassed {
		t.Fatalf("plan = %q, want passed", plan.Status)
	}
	// Submission exit fields were copied onto the step at pass.
	var exitFields string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT exit_fields::text FROM step_instance WHERE id = $1`, plan.ID).Scan(&exitFields); err != nil {
		t.Fatalf("read exit fields: %v", err)
	}
	if !strings.Contains(exitFields, "spec_url") {
		t.Fatalf("step exit_fields = %s, want spec_url copied", exitFields)
	}
	// The system-derived verdict exists.
	verdict, err := f.queries.GetVerdictByStepInstance(context.Background(), plan.ID)
	if err != nil {
		t.Fatalf("system verdict missing: %v", err)
	}
	if verdict.Result != VerdictPass || verdict.VerdictBy != "system" {
		t.Fatalf("verdict = %q by %q, want pass by system", verdict.Result, verdict.VerdictBy)
	}
	// Plan's child issue is done (service-layer lifecycle flip).
	planIssue, err := f.queries.GetIssue(context.Background(), plan.IssueID)
	if err != nil {
		t.Fatalf("get plan issue: %v", err)
	}
	if planIssue.Status != "done" {
		t.Fatalf("plan issue = %q, want done", planIssue.Status)
	}

	// Implement was promoted pending → active; review was pre-created.
	impl := f.latestStep(run.ID, "implement")
	if impl.Status != StepActive || impl.Attempt != 1 || !impl.AgentTaskID.Valid {
		t.Fatalf("implement = %q attempt %d, want active attempt 1 with task", impl.Status, impl.Attempt)
	}
	review := f.latestStep(run.ID, "review")
	if review.Status != StepPending {
		t.Fatalf("review = %q, want pending (pre-created one ahead)", review.Status)
	}
	transitions := f.transitionsForStep(impl.ID)
	if len(transitions) != 2 || transitions[0] != [2]string{"none", StepPending} || transitions[1] != [2]string{StepPending, StepActive} {
		t.Fatalf("implement transitions = %v, want none→pending, pending→active", transitions)
	}

	f.passExecutorStep(run.ID, "implement", nil)
	// Evaluator step: no system verdict — it waits for the evaluator's own.
	review = f.latestStep(run.ID, "review")
	if review.Status != StepActive {
		t.Fatalf("review = %q, want active", review.Status)
	}
	if _, err := f.queries.GetVerdictByStepInstance(context.Background(), review.ID); err == nil {
		t.Fatalf("evaluator step must not get a system verdict")
	}

	if _, err := f.engine.RecordVerdict(context.Background(), review.AgentTaskID, VerdictInput{Result: VerdictPass}); err != nil {
		t.Fatalf("record verdict: %v", err)
	}
	// Acceptance node: pending acceptance + run parked.
	if got := f.runStatus(run.ID); got != RunWaitingAcceptance {
		t.Fatalf("run = %q, want waiting_acceptance", got)
	}
	acc, err := f.queries.GetPendingAcceptanceByRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("pending acceptance missing: %v", err)
	}
	acceptStep := f.latestStep(run.ID, "accept")
	if acc.StepInstanceID != acceptStep.ID {
		t.Fatalf("acceptance bound to step %v, want accept step %v", acc.StepInstanceID, acceptStep.ID)
	}
}

func TestApproveAcceptanceCompletesRun(t *testing.T) {
	f := newTestFixture(t)
	tmpl := chainTemplate(f, "chain")
	run := f.startRun(tmpl, "ext-approve", "Approve flow")
	f.passExecutorStep(run.ID, "plan", map[string]any{"spec_url": "https://spec"})
	f.passExecutorStep(run.ID, "implement", nil)
	review := f.latestStep(run.ID, "review")
	if _, err := f.engine.RecordVerdict(context.Background(), review.AgentTaskID, VerdictInput{Result: VerdictPass}); err != nil {
		t.Fatalf("record verdict: %v", err)
	}
	acc, err := f.queries.GetPendingAcceptanceByRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("pending acceptance: %v", err)
	}
	if err := f.engine.ApproveAcceptance(context.Background(), run.ID, acc.ID, util.MustParseUUID(f.memberID)); err != nil {
		t.Fatalf("approve: %v", err)
	}

	if got := f.runStatus(run.ID); got != RunCompleted {
		t.Fatalf("run = %q, want completed", got)
	}
	for _, key := range []string{"plan", "implement", "review", "accept", "end"} {
		if got := f.latestStep(run.ID, key).Status; got != StepPassed {
			t.Fatalf("step %s = %q, want passed (full chain terminal)", key, got)
		}
	}
	intake, err := f.queries.GetIssue(context.Background(), run.IntakeIssueID)
	if err != nil {
		t.Fatalf("get intake: %v", err)
	}
	if intake.Status != "done" {
		t.Fatalf("intake = %q, want done", intake.Status)
	}
	if n := f.inboxCount("workflow_completed"); n != 1 {
		t.Fatalf("completion inbox = %d, want 1", n)
	}
	// Double-approve is a guarded conflict, not a re-advance.
	if err := f.engine.ApproveAcceptance(context.Background(), run.ID, acc.ID, util.MustParseUUID(f.memberID)); err != ErrAcceptanceConflict {
		t.Fatalf("re-approve = %v, want ErrAcceptanceConflict", err)
	}
}

func TestIdempotentResignalDoesNotDoubleAdvance(t *testing.T) {
	f := newTestFixture(t)
	tmpl := chainTemplate(f, "chain")
	run := f.startRun(tmpl, "ext-resignal", "Resignal")

	f.passExecutorStep(run.ID, "plan", map[string]any{"spec_url": "https://spec"})
	plan := f.latestStep(run.ID, "plan")

	// Duplicate signal: the step is terminal, so this must be a no-op.
	if err := f.engine.SignalVerdict(context.Background(), plan.ID); err != nil {
		t.Fatalf("re-signal: %v", err)
	}
	steps, err := f.queries.ListStepInstancesForRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("list steps: %v", err)
	}
	byNode := map[string]int{}
	for _, s := range steps {
		byNode[s.NodeKey]++
	}
	if byNode["plan"] != 1 || byNode["implement"] != 1 || byNode["review"] != 1 {
		t.Fatalf("steps per node = %v, want exactly 1 each (no double activation)", byNode)
	}
	// Guarded-update proof: only one passed transition exists for plan.
	var passedTransitions int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM step_transition WHERE step_instance_id = $1 AND to_status = 'passed'`, plan.ID).Scan(&passedTransitions); err != nil {
		t.Fatalf("count transitions: %v", err)
	}
	if passedTransitions != 1 {
		t.Fatalf("passed transitions = %d, want exactly 1", passedTransitions)
	}
}

func TestNullsNotDistinctRejectsDuplicateAttempt(t *testing.T) {
	f := newTestFixture(t)
	tmpl := chainTemplate(f, "chain")
	run := f.startRun(tmpl, "ext-nulls", "Nulls")

	// uq_step_instance_attempt is UNIQUE NULLS NOT DISTINCT (run_id,
	// node_key, parent_step_id, attempt): a plain UNIQUE would treat the
	// NULL parent as distinct and let the duplicate through (design.md §1).
	var insertErr error
	_, insertErr = f.pool.Exec(context.Background(), `
		INSERT INTO step_instance (run_id, node_key, status, attempt)
		VALUES ($1, 'plan', 'active', 1)
	`, run.ID)
	if insertErr == nil {
		t.Fatalf("duplicate (run, node, NULL parent, attempt) insert must be rejected")
	}
	if !isUniqueViolation(insertErr) {
		t.Fatalf("expected unique violation, got %v", insertErr)
	}
}

func TestFailVerdictRetriesWithNewAttempt(t *testing.T) {
	f := newTestFixture(t)
	tmpl := f.createPublishedTemplate("retry", []NodeInput{
		agentNode("work", RoleExecutor, "Executor Agent", NodeConfig{MaxAttempts: 2}),
		agentNode("gate", RoleEvaluator, "Evaluator Agent", NodeConfig{MaxAttempts: 2}),
		typedNode("end", NodeTypeEnd, NodeConfig{}),
	}, linearEdges("work", "gate", "end"))
	run := f.startRun(tmpl, "ext-retry", "Retry")

	f.passExecutorStep(run.ID, "work", nil)
	gate := f.latestStep(run.ID, "gate")
	if _, err := f.engine.RecordVerdict(context.Background(), gate.AgentTaskID, VerdictInput{
		Result: VerdictFail, RootCause: "tests missing",
	}); err != nil {
		t.Fatalf("record verdict: %v", err)
	}

	gate = f.latestStep(run.ID, "gate")
	if gate.Status != StepActive || gate.Attempt != 2 {
		t.Fatalf("gate = %q attempt %d, want active attempt 2 (retry)", gate.Status, gate.Attempt)
	}
	if got := f.runStatus(run.ID); got != RunRunning {
		t.Fatalf("run = %q, want running", got)
	}
	// Old attempt's child issue was cancelled.
	steps, err := f.queries.ListStepInstancesForRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("list steps: %v", err)
	}
	var attempt1 db.StepInstance
	for _, s := range steps {
		if s.NodeKey == "gate" && s.Attempt == 1 {
			attempt1 = s
		}
	}
	if attempt1.Status != StepFailed {
		t.Fatalf("attempt 1 = %q, want failed", attempt1.Status)
	}
	oldIssue, err := f.queries.GetIssue(context.Background(), attempt1.IssueID)
	if err != nil {
		t.Fatalf("get old issue: %v", err)
	}
	if oldIssue.Status != "cancelled" {
		t.Fatalf("old attempt issue = %q, want cancelled", oldIssue.Status)
	}
	// New attempt got its own child issue + task with the attempt suffix.
	newIssue, err := f.queries.GetIssue(context.Background(), gate.IssueID)
	if err != nil {
		t.Fatalf("get new issue: %v", err)
	}
	if !strings.HasSuffix(newIssue.Title, "-gate-attempt2") {
		t.Fatalf("new issue title = %q, want -gate-attempt2 suffix", newIssue.Title)
	}
	if n := f.countTasksForIssue(newIssue.ID); n != 1 {
		t.Fatalf("tasks on retry issue = %d, want 1", n)
	}
}

func TestFailVerdictEscalatesAfterMaxAttempts(t *testing.T) {
	f := newTestFixture(t)
	tmpl := f.createPublishedTemplate("escalate", []NodeInput{
		agentNode("work", RoleExecutor, "Executor Agent", NodeConfig{}),
		agentNode("gate", RoleEvaluator, "Evaluator Agent", NodeConfig{MaxAttempts: 1}),
		typedNode("end", NodeTypeEnd, NodeConfig{}),
	}, linearEdges("work", "gate", "end"))
	run := f.startRun(tmpl, "ext-escalate", "Escalate")

	f.passExecutorStep(run.ID, "work", nil)
	gate := f.latestStep(run.ID, "gate")
	if _, err := f.engine.RecordVerdict(context.Background(), gate.AgentTaskID, VerdictInput{
		Result: VerdictFail, RootCause: "unfixable",
	}); err != nil {
		t.Fatalf("record verdict: %v", err)
	}

	if got := f.runStatus(run.ID); got != RunPaused {
		t.Fatalf("run = %q, want paused (max attempts exhausted)", got)
	}
	gate = f.latestStep(run.ID, "gate")
	if gate.Status != StepFailed || gate.Attempt != 1 {
		t.Fatalf("gate = %q attempt %d, want failed attempt 1", gate.Status, gate.Attempt)
	}
	// Human handoff: intake reassigned to the initiator + inbox.
	intake, err := f.queries.GetIssue(context.Background(), run.IntakeIssueID)
	if err != nil {
		t.Fatalf("get intake: %v", err)
	}
	if intake.AssigneeType.String != "member" || intake.AssigneeID != util.MustParseUUID(f.userID) {
		t.Fatalf("intake assignee = %s/%v, want member/initiator", intake.AssigneeType.String, intake.AssigneeID)
	}
	if n := f.inboxCount("workflow_escalated"); n != 1 {
		t.Fatalf("escalation inbox = %d, want 1", n)
	}
}

func TestBlockedSubmissionPausesRun(t *testing.T) {
	f := newTestFixture(t)
	tmpl := chainTemplate(f, "chain")
	run := f.startRun(tmpl, "ext-blocked", "Blocked")

	plan := f.latestStep(run.ID, "plan")
	if _, _, err := f.engine.RecordSubmission(context.Background(), plan.AgentTaskID, SubmissionInput{
		Status:     SubmissionBlocked,
		RawSummary: "missing API key",
	}); err != nil {
		t.Fatalf("record submission: %v", err)
	}
	plan = f.latestStep(run.ID, "plan")
	if plan.Status != StepBlocked {
		t.Fatalf("plan = %q, want blocked", plan.Status)
	}
	if got := f.runStatus(run.ID); got != RunPaused {
		t.Fatalf("run = %q, want paused", got)
	}
	verdict, err := f.queries.GetVerdictByStepInstance(context.Background(), plan.ID)
	if err != nil {
		t.Fatalf("verdict missing: %v", err)
	}
	if verdict.Result != VerdictBlocked || verdict.RootCause.String != "missing API key" {
		t.Fatalf("verdict = %q cause %q", verdict.Result, verdict.RootCause.String)
	}
	if n := f.inboxCount("workflow_blocked"); n != 1 {
		t.Fatalf("blocked inbox = %d, want 1", n)
	}
}

func TestDoneWithConcernsPassesWithEvidence(t *testing.T) {
	f := newTestFixture(t)
	tmpl := chainTemplate(f, "chain")
	run := f.startRun(tmpl, "ext-concerns", "Concerns")

	plan := f.latestStep(run.ID, "plan")
	gaps := []byte(`["edge case untested"]`)
	if _, _, err := f.engine.RecordSubmission(context.Background(), plan.AgentTaskID, SubmissionInput{
		Status:     SubmissionDoneWithConcerns,
		Gaps:       gaps,
		ExitFields: map[string]any{"spec_url": "https://spec"},
	}); err != nil {
		t.Fatalf("record submission: %v", err)
	}
	verdict, err := f.queries.GetVerdictByStepInstance(context.Background(), plan.ID)
	if err != nil {
		t.Fatalf("verdict missing: %v", err)
	}
	if verdict.Result != VerdictPass {
		t.Fatalf("verdict = %q, want pass (concerns ride evidence)", verdict.Result)
	}
	if !strings.Contains(string(verdict.Evidence), "edge case untested") {
		t.Fatalf("evidence = %s, want concerns recorded", verdict.Evidence)
	}
	// And the chain advanced.
	if got := f.latestStep(run.ID, "implement").Status; got != StepActive {
		t.Fatalf("implement = %q, want active", got)
	}
}

func TestAcceptancePendingUniqueness(t *testing.T) {
	f := newTestFixture(t)
	tmpl := chainTemplate(f, "chain")
	run := f.startRun(tmpl, "ext-pending", "Pending")
	f.passExecutorStep(run.ID, "plan", map[string]any{"spec_url": "https://spec"})
	f.passExecutorStep(run.ID, "implement", nil)
	review := f.latestStep(run.ID, "review")
	if _, err := f.engine.RecordVerdict(context.Background(), review.AgentTaskID, VerdictInput{Result: VerdictPass}); err != nil {
		t.Fatalf("record verdict: %v", err)
	}
	acceptStep := f.latestStep(run.ID, "accept")

	// idx_acceptance_pending_step: a second pending acceptance for the same
	// step violates the partial unique index.
	var err error
	_, err = f.pool.Exec(context.Background(), `
		INSERT INTO acceptance (run_id, step_instance_id) VALUES ($1, $2)
	`, run.ID, acceptStep.ID)
	if err == nil || !isUniqueViolation(err) {
		t.Fatalf("second pending acceptance must hit unique index, got %v", err)
	}
	// Decided rows do NOT block a later round (partial index is pending-only).
	acc, aerr := f.queries.GetPendingAcceptanceByRun(context.Background(), run.ID)
	if aerr != nil {
		t.Fatalf("get pending: %v", aerr)
	}
	if _, err := f.queries.DecideAcceptance(context.Background(), db.DecideAcceptanceParams{
		NewStatus: "rejected", ID: acc.ID,
		RejectReason:    pgtype.Text{String: "redo", Valid: true},
		RejectToNodeKey: pgtype.Text{String: "plan", Valid: true},
	}); err != nil {
		t.Fatalf("decide: %v", err)
	}
	if _, err := f.queries.CreateAcceptance(context.Background(), db.CreateAcceptanceParams{
		RunID: run.ID, StepInstanceID: acceptStep.ID,
	}); err != nil {
		t.Fatalf("fresh pending acceptance after decision must succeed: %v", err)
	}
}

func TestAutoPassAcceptanceAdvances(t *testing.T) {
	f := newTestFixture(t)
	tmpl := f.createPublishedTemplate("autopass", []NodeInput{
		agentNode("work", RoleExecutor, "Executor Agent", NodeConfig{}),
		typedNode("accept", NodeTypeAcceptance, NodeConfig{AutoPass: true}),
		typedNode("end", NodeTypeEnd, NodeConfig{}),
	}, linearEdges("work", "accept", "end"))
	run := f.startRun(tmpl, "ext-autopass", "Autopass")
	f.passExecutorStep(run.ID, "work", nil)

	if got := f.runStatus(run.ID); got != RunCompleted {
		t.Fatalf("run = %q, want completed (auto_pass approved)", got)
	}
	accs, err := f.queries.ListAcceptancesForRun(context.Background(), run.ID)
	if err != nil || len(accs) != 1 {
		t.Fatalf("acceptances = %d, want 1", len(accs))
	}
	if accs[0].Status != "approved" {
		t.Fatalf("acceptance = %q, want approved", accs[0].Status)
	}
}
