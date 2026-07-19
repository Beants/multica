package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// engine_edge_test.go — Wave 1.7 coverage top-up: concurrency/idempotency
// races (blueprint §8), the verdict actor model's remaining branches
// (NEEDS_CONTEXT derivation, evaluator required exit fields), guarded-update
// race losses, paused-run signal no-ops, acceptance decision edge cases,
// activation failure landings, and pure-unit coverage of snapshot helpers /
// run-context parsing / system-verdict derivation.

// ---------------------------------------------------------------------------
// Pure unit tests (no DB)
// ---------------------------------------------------------------------------

func TestSnapshotHelpers(t *testing.T) {
	t.Parallel()

	if _, err := ParseSnapshot([]byte("{not json")); err == nil {
		t.Fatalf("malformed snapshot must be rejected")
	}
	var empty Snapshot
	if empty.StartNode() != nil {
		t.Fatalf("empty snapshot must have no start node")
	}

	snap := &Snapshot{
		Nodes: []SnapshotNode{{NodeKey: "a"}, {NodeKey: "b"}, {NodeKey: "c"}},
		Edges: []SnapshotEdge{
			{FromNodeKey: "a", ToNodeKey: "b", Priority: 2},
			{FromNodeKey: "a", ToNodeKey: "c", Priority: 1},
			{FromNodeKey: "b", ToNodeKey: "c", Priority: 1},
		},
	}
	if got := snap.StartNode(); got == nil || got.NodeKey != "a" {
		t.Fatalf("start node = %v, want a", got)
	}
	// Default-edge selection by priority: the lower priority value wins when
	// a (malformed) graph carries more than one outgoing edge.
	if got := snap.NextAfter("a"); got == nil || got.NodeKey != "c" {
		t.Fatalf("NextAfter(a) = %v, want c (priority 1 beats 2)", got)
	}
	if got := snap.NextAfter("c"); got != nil {
		t.Fatalf("NextAfter(c) = %v, want nil (chain tail)", got)
	}
	if snap.NodeByKey("zzz") != nil {
		t.Fatalf("NodeByKey(zzz) must be nil")
	}
	upstream := snap.UpstreamNodeKeys("c")
	if len(upstream) != 2 {
		t.Fatalf("UpstreamNodeKeys(c) = %v, want [b a]", upstream)
	}
	// Downstream walk stops at an already-seen node (cycle guard).
	loopy := &Snapshot{
		Nodes: []SnapshotNode{{NodeKey: "a"}, {NodeKey: "b"}},
		Edges: []SnapshotEdge{
			{FromNodeKey: "a", ToNodeKey: "b"},
			{FromNodeKey: "b", ToNodeKey: "a"},
		},
	}
	if got := loopy.DownstreamNodeKeys("a"); len(got) != 1 || got[0] != "b" {
		t.Fatalf("DownstreamNodeKeys(a) on a cycle = %v, want [b]", got)
	}
}

func TestRunContextParsing(t *testing.T) {
	t.Parallel()

	if rc := ParseRunContext(nil); rc.Initiator().Valid || rc.Reviewer().Valid {
		t.Fatalf("empty context must yield invalid IDs: %+v", rc)
	}
	// Malformed JSON is tolerated as an empty context (D-9).
	if rc := ParseRunContext([]byte("{bad")); rc.InitiatorID != "" {
		t.Fatalf("malformed context = %+v, want zero", rc)
	}
	// Unknown fields pass through; unparseable IDs stay invalid.
	rc := ParseRunContext([]byte(`{"initiator_id":"not-a-uuid","reviewer_id":"","future_knob":1}`))
	if rc.Initiator().Valid || rc.Reviewer().Valid {
		t.Fatalf("unparseable IDs must stay invalid: %+v", rc)
	}
	raw, err := json.Marshal(RunContext{InitiatorID: "11111111-1111-1111-1111-111111111111"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	rc = ParseRunContext(raw)
	if !rc.Initiator().Valid || util.UUIDToString(rc.Initiator()) != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("initiator round-trip = %v", rc.Initiator())
	}
	raw, err = json.Marshal(RunContext{ReviewerID: "22222222-2222-2222-2222-222222222222"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	rc = ParseRunContext(raw)
	if !rc.Reviewer().Valid || util.UUIDToString(rc.Reviewer()) != "22222222-2222-2222-2222-222222222222" {
		t.Fatalf("reviewer round-trip = %v", rc.Reviewer())
	}
}

func TestDeriveSystemVerdictMapping(t *testing.T) {
	t.Parallel()

	result, cause, evidence := deriveSystemVerdict(SubmissionInput{Status: SubmissionDone})
	if result != VerdictPass || cause != "" || evidence != nil {
		t.Fatalf("DONE = %q/%q/%s, want pass//nil", result, cause, evidence)
	}
	result, _, evidence = deriveSystemVerdict(SubmissionInput{
		Status: SubmissionDoneWithConcerns, Gaps: []byte(`["flaky test"]`),
	})
	if result != VerdictPass || !strings.Contains(string(evidence), "flaky test") {
		t.Fatalf("DONE_WITH_CONCERNS = %q ev=%s, want pass with concerns evidence", result, evidence)
	}
	// No gaps payload: the evidence normalizes to an empty array.
	_, _, evidence = deriveSystemVerdict(SubmissionInput{Status: SubmissionDoneWithConcerns})
	if !strings.Contains(string(evidence), "[]") {
		t.Fatalf("concerns without gaps: evidence = %s, want []", evidence)
	}
	result, cause, _ = deriveSystemVerdict(SubmissionInput{Status: SubmissionBlocked, RawSummary: "no creds"})
	if result != VerdictBlocked || cause != "no creds" {
		t.Fatalf("BLOCKED = %q/%q", result, cause)
	}
	_, cause, _ = deriveSystemVerdict(SubmissionInput{Status: SubmissionBlocked})
	if cause != "agent reported BLOCKED" {
		t.Fatalf("BLOCKED fallback cause = %q", cause)
	}
	result, cause, _ = deriveSystemVerdict(SubmissionInput{Status: SubmissionNeedsContext})
	if result != VerdictBlocked || cause != "agent reported NEEDS_CONTEXT" {
		t.Fatalf("NEEDS_CONTEXT = %q/%q", result, cause)
	}
	// Unreachable via RecordSubmission (status is validated first); the
	// defensive default still maps to blocked.
	result, cause, _ = deriveSystemVerdict(SubmissionInput{Status: "PARTIAL"})
	if result != VerdictBlocked || cause != "unknown submission status" {
		t.Fatalf("unknown = %q/%q", result, cause)
	}
}

func TestCompactJSON(t *testing.T) {
	t.Parallel()

	if got := compactJSON(map[string]any{"k": "v"}, 1024); !strings.Contains(got, `"k"`) {
		t.Fatalf("compactJSON = %q", got)
	}
	big := map[string]any{"k": strings.Repeat("x", 100)}
	if got := compactJSON(big, 10); len(got) > 10 {
		t.Fatalf("compactJSON truncation = %d bytes, want ≤ 10", len(got))
	}
	if got := compactJSON(make(chan int), 10); got != "(unavailable)" {
		t.Fatalf("unmarshalable value = %q, want (unavailable)", got)
	}
}

func TestIsUniqueViolationPlainErrors(t *testing.T) {
	t.Parallel()
	if isUniqueViolation(nil) || isUniqueViolation(errors.New("boom")) {
		t.Fatalf("non-pg errors must not read as unique violations")
	}
}

// ---------------------------------------------------------------------------
// Verdict actor model: NEEDS_CONTEXT derivation + evaluator exit fields
// ---------------------------------------------------------------------------

func TestNeedsContextSubmissionPausesWithFallbackCause(t *testing.T) {
	f := newTestFixture(t)
	tmpl := chainTemplate(f, "chain")
	run := f.startRun(tmpl, "ext-needsctx", "Needs context")

	plan := f.latestStep(run.ID, "plan")
	if _, _, err := f.engine.RecordSubmission(context.Background(), plan.AgentTaskID, SubmissionInput{
		Status: SubmissionNeedsContext,
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
	if verdict.Result != VerdictBlocked || verdict.RootCause.String != "agent reported NEEDS_CONTEXT" {
		t.Fatalf("verdict = %q cause %q, want blocked / fallback cause", verdict.Result, verdict.RootCause.String)
	}
	if n := f.inboxCount("workflow_blocked"); n != 1 {
		t.Fatalf("blocked inbox = %d, want 1", n)
	}
}

func TestEvaluatorVerdictRequiredExitFields(t *testing.T) {
	f := newTestFixture(t)
	tmpl := f.createPublishedTemplate("gate-fields", []NodeInput{
		agentNode("work", RoleExecutor, "Executor Agent", NodeConfig{}),
		agentNode("gate", RoleEvaluator, "Evaluator Agent", NodeConfig{
			ExitFields: &ExitFieldsSchema{Fields: []ExitFieldSpec{{Name: "verdict_notes", Type: "string", Required: true}}},
		}),
		typedNode("end", NodeTypeEnd, NodeConfig{}),
	}, linearEdges("work", "gate", "end"))
	run := f.startRun(tmpl, "ext-gatefields", "Gate fields")
	ctx := context.Background()

	f.passExecutorStep(run.ID, "work", nil)
	gate := f.latestStep(run.ID, "gate")

	// The auto-created minimal submission must pass the SAME 准出 validation:
	// omitting the node's required exit fields is a structured 400-class error.
	var validationErr *ExitFieldsValidationError
	_, err := f.engine.RecordVerdict(ctx, gate.AgentTaskID, VerdictInput{Result: VerdictPass})
	if !errors.As(err, &validationErr) {
		t.Fatalf("verdict without required exit fields = %v, want ExitFieldsValidationError", err)
	}
	if len(validationErr.Fields) != 1 || validationErr.Fields[0].Code != "missing" || validationErr.Fields[0].Name != "verdict_notes" {
		t.Fatalf("fields = %+v, want one missing verdict_notes", validationErr.Fields)
	}
	// Nothing was written: no submission, no verdict, step still active.
	if _, err := f.queries.GetSubmissionByStepInstance(ctx, gate.ID); err == nil {
		t.Fatalf("rejected verdict must not leave a submission behind")
	}
	if got := f.latestStep(run.ID, "gate").Status; got != StepActive {
		t.Fatalf("gate = %q, want still active", got)
	}

	// Providing the required fields lets the auto-submission + verdict through.
	v, err := f.engine.RecordVerdict(ctx, gate.AgentTaskID, VerdictInput{
		Result:     VerdictPass,
		ExitFields: map[string]any{"verdict_notes": "looks good"},
	})
	if err != nil {
		t.Fatalf("record verdict: %v", err)
	}
	if v.VerdictBy != "agent" {
		t.Fatalf("verdict_by = %q, want agent", v.VerdictBy)
	}
	sub, err := f.queries.GetSubmissionByStepInstance(ctx, gate.ID)
	if err != nil {
		t.Fatalf("auto-created submission missing: %v", err)
	}
	if !strings.Contains(string(sub.ExitFields), "verdict_notes") {
		t.Fatalf("submission exit_fields = %s, want verdict_notes", sub.ExitFields)
	}
	if got := f.runStatus(run.ID); got != RunCompleted {
		t.Fatalf("run = %q, want completed", got)
	}
}

// ---------------------------------------------------------------------------
// 并发与幂等 (blueprint §8)
// ---------------------------------------------------------------------------

// A verdict landing while the run is paused must not advance the machine;
// re-signalling after the unpause drives the advancement instead.
func TestVerdictOnPausedRunIsNoOp(t *testing.T) {
	f := newTestFixture(t)
	tmpl := chainTemplate(f, "chain")
	run := f.startRun(tmpl, "ext-paused-signal", "Paused signal")
	ctx := context.Background()

	f.passExecutorStep(run.ID, "plan", map[string]any{"spec_url": "https://spec"})
	impl := f.latestStep(run.ID, "implement")

	// A human pauses the run before the agent's submission lands.
	if _, err := f.queries.UpdateWorkflowRunStatus(ctx, db.UpdateWorkflowRunStatusParams{
		NewStatus: RunPaused, ID: run.ID, ExpectedStatus: RunRunning,
	}); err != nil {
		t.Fatalf("pause run: %v", err)
	}
	if _, created, err := f.engine.RecordSubmission(ctx, impl.AgentTaskID, SubmissionInput{Status: SubmissionDone}); err != nil || !created {
		t.Fatalf("record submission: %v created=%v", err, created)
	}
	// The system verdict exists but was NOT consumed: no advance while paused.
	impl = f.latestStep(run.ID, "implement")
	if impl.Status != StepActive {
		t.Fatalf("implement = %q, want still active (paused run ignores the signal)", impl.Status)
	}
	if got := f.latestStep(run.ID, "review").Status; got != StepPending {
		t.Fatalf("review = %q, want pending (no advance)", got)
	}

	// After the unpause, re-signalling consumes the parked verdict.
	if _, err := f.queries.UpdateWorkflowRunStatus(ctx, db.UpdateWorkflowRunStatusParams{
		NewStatus: RunRunning, ID: run.ID, ExpectedStatus: RunPaused,
	}); err != nil {
		t.Fatalf("unpause run: %v", err)
	}
	if err := f.engine.SignalVerdict(ctx, impl.ID); err != nil {
		t.Fatalf("re-signal: %v", err)
	}
	if got := f.latestStep(run.ID, "implement").Status; got != StepPassed {
		t.Fatalf("implement = %q, want passed after re-signal", got)
	}
	if got := f.latestStep(run.ID, "review").Status; got != StepActive {
		t.Fatalf("review = %q, want active (advanced)", got)
	}
}

// The guarded UPDATE reports a lost race as false so the caller abandons —
// the other consumer's committed transition is respected.
func TestTransitionStepTxLostRace(t *testing.T) {
	f := newTestFixture(t)
	tmpl := chainTemplate(f, "chain")
	run := f.startRun(tmpl, "ext-race", "Guard race")
	ctx := context.Background()

	plan := f.latestStep(run.ID, "plan")
	// A competing consumer moves the step active → passed first.
	if _, err := f.queries.UpdateStepInstanceStatus(ctx, db.UpdateStepInstanceStatusParams{
		NewStatus: StepPassed, ID: plan.ID, ExpectedStatus: StepActive,
	}); err != nil {
		t.Fatalf("competing transition: %v", err)
	}

	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback(ctx)
	qtx := f.queries.WithTx(tx)
	// The stale read (still 'active') loses the guard: zero rows → false.
	if f.engine.transitionStepTx(ctx, qtx, plan, StepBlocked, "test", nil) {
		t.Fatalf("guarded transition must report the lost race")
	}
}

// Concurrent pushes for the same external source converge on one run: the
// UNIQUE(workspace, source_type, source_id, template) slot admits exactly one
// creator and every loser returns that run (pre-check or post-collision
// re-read — blueprint §8.3).
func TestStartRunConcurrentIdempotent(t *testing.T) {
	f := newTestFixture(t)
	tmpl := chainTemplate(f, "chain")
	ctx := context.Background()

	const pushers = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	createdCount := 0
	runIDs := map[string]bool{}
	errs := make([]error, 0)
	for i := 0; i < pushers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			run, created, err := f.engine.StartRun(ctx, StartRunParams{
				WorkspaceID: util.MustParseUUID(f.workspaceID),
				TemplateID:  tmpl.Template.ID,
				SourceType:  "hook",
				SourceID:    "ext-concurrent",
				Title:       "Concurrent requirement",
				InitiatorID: util.MustParseUUID(f.userID),
			})
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			if created {
				createdCount++
			}
			runIDs[util.UUIDToString(run.ID)] = true
		}()
	}
	wg.Wait()

	if len(errs) != 0 {
		t.Fatalf("concurrent StartRun errors: %v", errs)
	}
	if createdCount != 1 {
		t.Fatalf("created count = %d, want exactly 1", createdCount)
	}
	if len(runIDs) != 1 {
		t.Fatalf("distinct run IDs = %d, want 1 (all pushers converge)", len(runIDs))
	}
	// Exactly one intake + one plan child issue, one dispatch.
	var issueCount int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM issue WHERE workspace_id = $1`, f.workspaceID).Scan(&issueCount); err != nil {
		t.Fatalf("count issues: %v", err)
	}
	if issueCount != 2 {
		t.Fatalf("issues = %d, want 2 (single intake + single plan child)", issueCount)
	}
	var runID string
	for id := range runIDs {
		runID = id
	}
	plan := f.latestStep(util.MustParseUUID(runID), "plan")
	if n := f.countTasksForIssue(plan.IssueID); n != 1 {
		t.Fatalf("tasks on plan child = %d, want exactly 1", n)
	}
}

// Concurrent same-key submissions on one step collapse to a single row and a
// single advancement — the losers replay (pre-check or post-collision).
func TestRecordSubmissionConcurrentSameKey(t *testing.T) {
	f := newTestFixture(t)
	tmpl := chainTemplate(f, "chain")
	run := f.startRun(tmpl, "ext-subrace", "Submission race")
	ctx := context.Background()

	plan := f.latestStep(run.ID, "plan")
	const posters = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	createdCount := 0
	subIDs := map[string]bool{}
	errs := make([]error, 0)
	for i := 0; i < posters; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sub, created, err := f.engine.RecordSubmission(ctx, plan.AgentTaskID, SubmissionInput{
				Status:         SubmissionDone,
				ExitFields:     map[string]any{"spec_url": "https://spec"},
				IdempotencyKey: "ck",
			})
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			if created {
				createdCount++
			}
			subIDs[util.UUIDToString(sub.ID)] = true
		}()
	}
	wg.Wait()

	if len(errs) != 0 {
		t.Fatalf("concurrent submissions errors: %v", errs)
	}
	if createdCount != 1 {
		t.Fatalf("created count = %d, want exactly 1", createdCount)
	}
	if len(subIDs) != 1 {
		t.Fatalf("distinct submission IDs = %d, want 1 (all replays return the original)", len(subIDs))
	}
	var subCount int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM submission WHERE step_instance_id = $1`, plan.ID).Scan(&subCount); err != nil {
		t.Fatalf("count submissions: %v", err)
	}
	if subCount != 1 {
		t.Fatalf("submissions = %d, want 1", subCount)
	}
	// Advanced exactly once: one passed transition, one implement activation.
	plan = f.latestStep(run.ID, "plan")
	var passedTransitions int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM step_transition WHERE step_instance_id = $1 AND to_status = 'passed'`, plan.ID).Scan(&passedTransitions); err != nil {
		t.Fatalf("count transitions: %v", err)
	}
	if passedTransitions != 1 {
		t.Fatalf("passed transitions = %d, want exactly 1", passedTransitions)
	}
	steps, err := f.queries.ListStepInstancesForRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("list steps: %v", err)
	}
	implSteps := 0
	for _, s := range steps {
		if s.NodeKey == "implement" {
			implSteps++
		}
	}
	if implSteps != 1 {
		t.Fatalf("implement steps = %d, want 1 (no double advance)", implSteps)
	}
}

// ---------------------------------------------------------------------------
// Acceptance decision edges (decideAcceptanceTx guards, atomic rejection)
// ---------------------------------------------------------------------------

func TestDecideAcceptanceEdges(t *testing.T) {
	f := newTestFixture(t)
	tmpl := chainTemplate(f, "chain")
	ctx := context.Background()

	runA := f.startRun(tmpl, "ext-acc-a", "Run A")
	accA := driveToAcceptance(f, runA)
	runB := f.startRun(tmpl, "ext-acc-b", "Run B")
	accB := driveToAcceptance(f, runB)

	// An acceptance decided against the WRONG run is refused and stays pending.
	if err := f.engine.ApproveAcceptance(ctx, runA.ID, accB.ID, util.MustParseUUID(f.memberID)); !errors.Is(err, ErrAcceptanceNotFound) {
		t.Fatalf("approve with foreign run = %v, want ErrAcceptanceNotFound", err)
	}
	if got, err := f.queries.GetAcceptance(ctx, accB.ID); err != nil || got.Status != "pending" {
		t.Fatalf("accB = %q err=%v, want still pending", got.Status, err)
	}

	// A pending acceptance while the run is NOT waiting (concurrent unpark) is
	// refused — and the guarded decide rolls back, leaving it pending.
	if _, err := f.queries.UpdateWorkflowRunStatus(ctx, db.UpdateWorkflowRunStatusParams{
		NewStatus: RunRunning, ID: runA.ID, ExpectedStatus: RunWaitingAcceptance,
	}); err != nil {
		t.Fatalf("unpark run: %v", err)
	}
	if err := f.engine.ApproveAcceptance(ctx, runA.ID, accA.ID, util.MustParseUUID(f.memberID)); !errors.Is(err, ErrRunNotActive) {
		t.Fatalf("approve on unparked run = %v, want ErrRunNotActive", err)
	}
	if got, err := f.queries.GetAcceptance(ctx, accA.ID); err != nil || got.Status != "pending" {
		t.Fatalf("accA = %q err=%v, want still pending (atomic rollback)", got.Status, err)
	}

	// Rejecting toward a node whose latest step is IN FLIGHT (the pre-created
	// end row is pending, not terminal) is refused atomically — the acceptance
	// is not consumed by the failed reject.
	if err := f.engine.RejectAcceptance(ctx, runB.ID, accB.ID, util.MustParseUUID(f.memberID), "end", "redo the tail"); !errors.Is(err, ErrReworkTargetActive) {
		t.Fatalf("reject to in-flight target = %v, want ErrReworkTargetActive", err)
	}
	if got, err := f.queries.GetAcceptance(ctx, accB.ID); err != nil || got.Status != "pending" {
		t.Fatalf("accB after failed reject = %q err=%v, want still pending", got.Status, err)
	}
	if got := f.runStatus(runB.ID); got != RunWaitingAcceptance {
		t.Fatalf("run B = %q, want still waiting_acceptance (reject was atomic)", got)
	}
}

// A mid-chain acceptance (Spec Freeze) advances the chain on approve instead
// of completing the run.
func TestApproveMidChainAcceptanceAdvances(t *testing.T) {
	f := newTestFixture(t)
	tmpl := f.createPublishedTemplate("freeze", []NodeInput{
		agentNode("plan", RoleExecutor, "Executor Agent", NodeConfig{}),
		typedNode("freeze", NodeTypeAcceptance, NodeConfig{}),
		agentNode("impl", RoleExecutor, "Executor Agent", NodeConfig{}),
		typedNode("end", NodeTypeEnd, NodeConfig{}),
	}, linearEdges("plan", "freeze", "impl", "end"))
	run := f.startRun(tmpl, "ext-freeze", "Spec freeze")
	ctx := context.Background()

	f.passExecutorStep(run.ID, "plan", nil)
	if got := f.runStatus(run.ID); got != RunWaitingAcceptance {
		t.Fatalf("run = %q, want waiting_acceptance at freeze", got)
	}
	acc, err := f.queries.GetPendingAcceptanceByRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("pending acceptance: %v", err)
	}
	if err := f.engine.ApproveAcceptance(ctx, run.ID, acc.ID, util.MustParseUUID(f.memberID)); err != nil {
		t.Fatalf("approve freeze: %v", err)
	}

	if got := f.runStatus(run.ID); got != RunRunning {
		t.Fatalf("run = %q, want running after freeze approve", got)
	}
	freeze := f.latestStep(run.ID, "freeze")
	if freeze.Status != StepPassed {
		t.Fatalf("freeze = %q, want passed", freeze.Status)
	}
	transitions := f.transitionsForStep(freeze.ID)
	if n := len(transitions); n == 0 || transitions[n-1] != [2]string{StepActive, StepPassed} {
		t.Fatalf("freeze transitions = %v, want active→passed", transitions)
	}
	impl := f.latestStep(run.ID, "impl")
	if impl.Status != StepActive || !impl.AgentTaskID.Valid {
		t.Fatalf("impl = %q task=%v, want active with task", impl.Status, impl.AgentTaskID)
	}
	if got := f.latestStep(run.ID, "end").Status; got != StepPending {
		t.Fatalf("end = %q, want pending (pre-created one ahead)", got)
	}

	// The rest of the chain completes normally.
	f.passExecutorStep(run.ID, "impl", nil)
	if got := f.runStatus(run.ID); got != RunCompleted {
		t.Fatalf("run = %q, want completed", got)
	}
}

// An acceptance at the chain tail (no end node) completes the run on approve.
func TestApproveTailAcceptanceCompletes(t *testing.T) {
	f := newTestFixture(t)
	tmpl := f.createPublishedTemplate("tail-accept", []NodeInput{
		agentNode("work", RoleExecutor, "Executor Agent", NodeConfig{}),
		typedNode("accept", NodeTypeAcceptance, NodeConfig{}),
	}, linearEdges("work", "accept"))
	run := f.startRun(tmpl, "ext-tailacc", "Tail acceptance")
	ctx := context.Background()

	f.passExecutorStep(run.ID, "work", nil)
	acc, err := f.queries.GetPendingAcceptanceByRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("pending acceptance: %v", err)
	}
	if err := f.engine.ApproveAcceptance(ctx, run.ID, acc.ID, util.MustParseUUID(f.memberID)); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if got := f.runStatus(run.ID); got != RunCompleted {
		t.Fatalf("run = %q, want completed", got)
	}
	if got := f.latestStep(run.ID, "accept").Status; got != StepPassed {
		t.Fatalf("accept = %q, want passed", got)
	}
	intake, err := f.queries.GetIssue(ctx, run.IntakeIssueID)
	if err != nil {
		t.Fatalf("get intake: %v", err)
	}
	if intake.Status != "done" {
		t.Fatalf("intake = %q, want done", intake.Status)
	}
	if n := f.inboxCount("workflow_completed"); n != 1 {
		t.Fatalf("completion inbox = %d, want 1", n)
	}
}

// ---------------------------------------------------------------------------
// Activation failure + notification edge landings
// ---------------------------------------------------------------------------

// A snapshot node whose frozen agent_id was stripped (template republished
// between snapshot and activation) fails activation into blocked + paused
// instead of panicking on the empty UUID.
func TestMissingFrozenAgentIDBlocksActivation(t *testing.T) {
	f := newTestFixture(t)
	tmpl := f.createPublishedTemplate("no-agent", []NodeInput{
		agentNode("plan", RoleExecutor, "Executor Agent", NodeConfig{}),
		agentNode("work", RoleExecutor, "Executor Agent", NodeConfig{}),
		typedNode("end", NodeTypeEnd, NodeConfig{}),
	}, linearEdges("plan", "work", "end"))
	run := f.startRun(tmpl, "ext-noagent", "No frozen agent")
	ctx := context.Background()

	// Strip the frozen agent_id of the SECOND node inside the run snapshot.
	var snapRaw []byte
	if err := f.pool.QueryRow(ctx, `SELECT template_snapshot FROM workflow_run WHERE id = $1`, run.ID).Scan(&snapRaw); err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	snap, err := ParseSnapshot(snapRaw)
	if err != nil {
		t.Fatalf("parse snapshot: %v", err)
	}
	for i := range snap.Nodes {
		if snap.Nodes[i].NodeKey == "work" {
			snap.Nodes[i].Config.AgentID = ""
		}
	}
	patched, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE workflow_run SET template_snapshot = $2 WHERE id = $1`, run.ID, patched); err != nil {
		t.Fatalf("patch snapshot: %v", err)
	}

	f.passExecutorStep(run.ID, "plan", nil)

	work := f.latestStep(run.ID, "work")
	if work.Status != StepBlocked {
		t.Fatalf("work = %q, want blocked (activation failure landing)", work.Status)
	}
	if got := f.runStatus(run.ID); got != RunPaused {
		t.Fatalf("run = %q, want paused", got)
	}
	if n := f.inboxCount("workflow_blocked"); n != 1 {
		t.Fatalf("blocked inbox = %d, want 1", n)
	}
}

// A system-triggered run has no initiator: pause notifications are skipped
// instead of crashing on the invalid recipient.
func TestNoInitiatorSkipsNotification(t *testing.T) {
	f := newTestFixture(t)
	tmpl := chainTemplate(f, "chain")
	ctx := context.Background()

	run, created, err := f.engine.StartRun(ctx, StartRunParams{
		WorkspaceID: util.MustParseUUID(f.workspaceID),
		TemplateID:  tmpl.Template.ID,
		SourceType:  "manual",
		Title:       "System-triggered run",
	})
	if err != nil || !created {
		t.Fatalf("start run: %v created=%v", err, created)
	}
	plan := f.latestStep(run.ID, "plan")
	if _, _, err := f.engine.RecordSubmission(ctx, plan.AgentTaskID, SubmissionInput{Status: SubmissionBlocked}); err != nil {
		t.Fatalf("record submission: %v", err)
	}
	if got := f.runStatus(run.ID); got != RunPaused {
		t.Fatalf("run = %q, want paused", got)
	}
	if n := f.inboxCount("workflow_blocked"); n != 0 {
		t.Fatalf("blocked inbox = %d, want 0 (no initiator to notify)", n)
	}
}

// Late transitions on a completed run are no-ops; the handoff on an
// already-paused run skips the redundant pause write.
func TestLateTransitionsOnTerminalRunAreNoOps(t *testing.T) {
	f := newTestFixture(t)
	ctx := context.Background()

	tmpl := f.createPublishedTemplate("late", []NodeInput{
		agentNode("work", RoleExecutor, "Executor Agent", NodeConfig{}),
		typedNode("end", NodeTypeEnd, NodeConfig{}),
	}, linearEdges("work", "end"))
	run := f.startRun(tmpl, "ext-late", "Late transitions")
	f.passExecutorStep(run.ID, "work", nil)
	if got := f.runStatus(run.ID); got != RunCompleted {
		t.Fatalf("run = %q, want completed", got)
	}

	// A circuit-breaker landing after completion respects the terminal state.
	if err := f.engine.pauseAndHandoff(ctx, run, "too late", "workflow_circuit_breaker"); err != nil {
		t.Fatalf("pauseAndHandoff on completed run: %v", err)
	}
	if n := f.inboxCount("workflow_circuit_breaker"); n != 0 {
		t.Fatalf("breaker inbox = %d, want 0 (completed run is left alone)", n)
	}
	// Re-completing is idempotent (no duplicate completion inbox).
	if err := f.engine.completeRun(ctx, run); err != nil {
		t.Fatalf("completeRun again: %v", err)
	}
	if n := f.inboxCount("workflow_completed"); n != 1 {
		t.Fatalf("completion inbox = %d, want still 1", n)
	}

	// On an already-paused run the handoff skips the pause write but still
	// notifies the human.
	chain := chainTemplate(f, "chain2")
	run2 := f.startRun(chain, "ext-late2", "Paused handoff")
	plan := f.latestStep(run2.ID, "plan")
	if _, _, err := f.engine.RecordSubmission(ctx, plan.AgentTaskID, SubmissionInput{Status: SubmissionBlocked}); err != nil {
		t.Fatalf("record submission: %v", err)
	}
	if got := f.runStatus(run2.ID); got != RunPaused {
		t.Fatalf("run2 = %q, want paused", got)
	}
	if err := f.engine.pauseAndHandoff(ctx, run2, "still stuck", "workflow_circuit_breaker"); err != nil {
		t.Fatalf("pauseAndHandoff on paused run: %v", err)
	}
	if got := f.runStatus(run2.ID); got != RunPaused {
		t.Fatalf("run2 = %q, want still paused", got)
	}
	if n := f.inboxCount("workflow_circuit_breaker"); n != 1 {
		t.Fatalf("breaker inbox = %d, want 1 (handoff still lands)", n)
	}
}

// Duplicate acceptance activation is a no-op (partial unique index), and
// parking fails cleanly when the run has already moved on.
func TestAcceptanceActivationDuplicateAndMovedOn(t *testing.T) {
	f := newTestFixture(t)
	tmpl := chainTemplate(f, "chain")
	run := f.startRun(tmpl, "ext-accdup", "Acceptance activation edges")
	ctx := context.Background()
	driveToAcceptance(f, run)

	snap, err := ParseSnapshot(run.TemplateSnapshot)
	if err != nil {
		t.Fatalf("parse snapshot: %v", err)
	}
	node := snap.NodeByKey("accept")
	if node == nil {
		t.Fatalf("accept node missing from snapshot")
	}
	acceptStep := f.latestStep(run.ID, "accept")

	// Second activation for the same step hits idx_acceptance_pending_step.
	if err := f.engine.activateAcceptanceNode(ctx, run, node, acceptStep); err != nil {
		t.Fatalf("duplicate acceptance activation: %v", err)
	}
	accs, err := f.queries.ListAcceptancesForRun(ctx, run.ID)
	if err != nil || len(accs) != 1 {
		t.Fatalf("acceptances = %d, want 1 (duplicate was a no-op)", len(accs))
	}

	// Run moved on (paused) before a fresh acceptance step's activation parks
	// it: the guarded park misses and the whole activation rolls back.
	fresh, err := f.queries.CreateStepInstance(ctx, db.CreateStepInstanceParams{
		RunID: run.ID, NodeKey: "accept", Status: StepActive, Attempt: 2,
	})
	if err != nil {
		t.Fatalf("create fresh accept step: %v", err)
	}
	if _, err := f.queries.UpdateWorkflowRunStatus(ctx, db.UpdateWorkflowRunStatusParams{
		NewStatus: RunPaused, ID: run.ID, ExpectedStatus: RunWaitingAcceptance,
	}); err != nil {
		t.Fatalf("pause run: %v", err)
	}
	if err := f.engine.activateAcceptanceNode(ctx, run, node, fresh); err != nil {
		t.Fatalf("activation on moved-on run: %v", err)
	}
	accs, err = f.queries.ListAcceptancesForRun(ctx, run.ID)
	if err != nil || len(accs) != 1 {
		t.Fatalf("acceptances = %d, want still 1 (rolled back)", len(accs))
	}
}

// A node deleted from the snapshot between snapshot and signal surfaces as a
// structured error, and resignal only logs it (the submission write already
// committed).
func TestNodeMissingFromSnapshot(t *testing.T) {
	f := newTestFixture(t)
	tmpl := chainTemplate(f, "chain")
	run := f.startRun(tmpl, "ext-nonode", "Node removed")
	ctx := context.Background()

	var snapRaw []byte
	if err := f.pool.QueryRow(ctx, `SELECT template_snapshot FROM workflow_run WHERE id = $1`, run.ID).Scan(&snapRaw); err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	snap, err := ParseSnapshot(snapRaw)
	if err != nil {
		t.Fatalf("parse snapshot: %v", err)
	}
	nodes := snap.Nodes[:0]
	for _, n := range snap.Nodes {
		if n.NodeKey != "plan" {
			nodes = append(nodes, n)
		}
	}
	snap.Nodes = nodes
	patched, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE workflow_run SET template_snapshot = $2 WHERE id = $1`, run.ID, patched); err != nil {
		t.Fatalf("patch snapshot: %v", err)
	}

	plan := f.latestStep(run.ID, "plan")
	if _, _, err := f.engine.RecordSubmission(ctx, plan.AgentTaskID, SubmissionInput{
		Status: SubmissionDone, ExitFields: map[string]any{"spec_url": "https://spec"},
	}); err == nil || !strings.Contains(err.Error(), "missing from run snapshot") {
		t.Fatalf("submission with missing node = %v, want snapshot error", err)
	}
	// resignal swallows the same class of error (warn-only path) — no panic.
	f.engine.resignal(ctx, plan)
}

// ---------------------------------------------------------------------------
// Helper edges (tx-scoped + notification fallbacks)
// ---------------------------------------------------------------------------

func TestEngineHelperEdges(t *testing.T) {
	f := newTestFixture(t)
	tmpl := chainTemplate(f, "chain")
	run := f.startRun(tmpl, "ext-helpers", "Helper edges")
	ctx := context.Background()

	// Pre-creating an already-pending node is idempotent.
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	qtx := f.queries.WithTx(tx)
	if err := preCreateStepTx(ctx, qtx, run.ID, "implement"); err != nil {
		t.Fatalf("pre-create existing pending: %v", err)
	}
	tx.Rollback(ctx)

	// No passed upstream step yet → no exit fields to surface.
	if fields := f.engine.passedExitFields(ctx, run.ID, "plan"); fields != nil {
		t.Fatalf("passedExitFields = %v, want nil (plan not passed)", fields)
	}
	// A run without an intake issue falls back to its ID as the title.
	if got := intakeTitle(ctx, f.queries, db.WorkflowRun{ID: run.ID}); got != util.UUIDToString(run.ID) {
		t.Fatalf("intakeTitle fallback = %q, want run ID %q", got, util.UUIDToString(run.ID))
	}
	// nil details normalize to an empty object carrying the run_id.
	f.engine.notifyMember(ctx, run, util.MustParseUUID(f.userID), "workflow_test_notice", "info", "helper edge", nil)
	var n int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM inbox_item WHERE workspace_id = $1 AND type = 'workflow_test_notice'`, f.workspaceID).Scan(&n); err != nil {
		t.Fatalf("count inbox: %v", err)
	}
	if n != 1 {
		t.Fatalf("notice inbox = %d, want 1", n)
	}
	// A nil bus skips publishing (no panic).
	(&Engine{}).publishIssueCreated(db.Issue{})

	// Unknown step IDs surface as ErrStepNotFound.
	if err := f.engine.SignalVerdict(ctx, util.MustParseUUID("00000000-0000-0000-0000-000000000099")); !errors.Is(err, ErrStepNotFound) {
		t.Fatalf("signal unknown step = %v, want ErrStepNotFound", err)
	}
	// closeStepIssue best-effort paths: no issue linked, no issue service, and
	// a dangling issue reference (warn-only) — none may panic.
	f.engine.closeStepIssue(ctx, db.StepInstance{}, "done")
	(&Engine{}).closeStepIssue(ctx, db.StepInstance{IssueID: util.MustParseUUID("00000000-0000-0000-0000-000000000099")}, "done")
	f.engine.closeStepIssue(ctx, db.StepInstance{IssueID: util.MustParseUUID("00000000-0000-0000-0000-000000000099")}, "done")

	// auto_pass precondition reads: unknown run and un-passed upstream both
	// read as false.
	if f.engine.allUpstreamPassed(ctx, util.MustParseUUID("00000000-0000-0000-0000-000000000099"), "accept") {
		t.Fatalf("unknown run must read as not-all-passed")
	}
	if f.engine.allUpstreamPassed(ctx, run.ID, "review") {
		t.Fatalf("plan not passed yet — upstream must read as false")
	}
}

// ---------------------------------------------------------------------------
// Template create/update validation
// ---------------------------------------------------------------------------

func TestTemplateCreateUpdateValidation(t *testing.T) {
	f := newTestFixture(t)
	ctx := context.Background()
	wsID := util.MustParseUUID(f.workspaceID)

	if _, err := f.templates.CreateTemplate(ctx, CreateTemplateParams{
		WorkspaceID: wsID, Name: "no key", CreatedBy: util.MustParseUUID(f.userID),
		Nodes: []NodeInput{agentNode("work", RoleExecutor, "Executor Agent", NodeConfig{})},
	}); err == nil {
		t.Fatalf("empty key must be rejected")
	}
	if _, err := f.templates.CreateTemplate(ctx, CreateTemplateParams{
		WorkspaceID: wsID, Key: "no-name", CreatedBy: util.MustParseUUID(f.userID),
		Nodes: []NodeInput{agentNode("work", RoleExecutor, "Executor Agent", NodeConfig{})},
	}); err == nil {
		t.Fatalf("empty name must be rejected")
	}

	draft := f.createDraft(ctx, "upd-validate")
	// ReplaceGraph with an invalid graph fails before any write.
	if _, err := f.templates.UpdateTemplate(ctx, UpdateTemplateParams{
		WorkspaceID: wsID, TemplateID: draft.Template.ID, ReplaceGraph: true,
		Nodes: []NodeInput{
			agentNode("a", RoleExecutor, "Executor Agent", NodeConfig{}),
			agentNode("b", RoleExecutor, "Executor Agent", NodeConfig{}),
			agentNode("c", RoleExecutor, "Executor Agent", NodeConfig{}),
		},
		Edges: []EdgeInput{{FromNodeKey: "a", ToNodeKey: "b"}, {FromNodeKey: "a", ToNodeKey: "c"}},
	}); err == nil || !strings.Contains(err.Error(), "outgoing edge") {
		t.Fatalf("invalid replacement graph = %v, want out-degree error", err)
	}
	// A template owned by another workspace reads as not-found.
	name := "cross-workspace"
	if _, err := f.templates.UpdateTemplate(ctx, UpdateTemplateParams{
		WorkspaceID: util.MustParseUUID("00000000-0000-0000-0000-000000000099"),
		TemplateID:  draft.Template.ID, Name: &name,
	}); !errors.Is(err, ErrTemplateNotFound) {
		t.Fatalf("cross-workspace update = %v, want ErrTemplateNotFound", err)
	}
	// Unknown template IDs surface as not-draft (the guarded UPDATE found nothing).
	if _, err := f.templates.UpdateTemplate(ctx, UpdateTemplateParams{
		WorkspaceID: wsID,
		TemplateID:  util.MustParseUUID("00000000-0000-0000-0000-000000000099"), Name: &name,
	}); !errors.Is(err, ErrTemplateNotDraft) {
		t.Fatalf("unknown template update = %v, want ErrTemplateNotDraft", err)
	}
}

// ---------------------------------------------------------------------------
// Run pause guards + acceptance/step consistency edges
// ---------------------------------------------------------------------------

// pauseRunTx's second guard iteration: a run parked in waiting_acceptance
// pauses without a running-status precondition.
func TestPauseRunTxFromWaitingAcceptance(t *testing.T) {
	f := newTestFixture(t)
	tmpl := chainTemplate(f, "chain")
	run := f.startRun(tmpl, "ext-pausewait", "Pause from waiting")
	ctx := context.Background()
	driveToAcceptance(f, run)

	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback(ctx)
	if !f.engine.pauseRunTx(ctx, f.queries.WithTx(tx), run) {
		t.Fatalf("pause from waiting_acceptance must succeed")
	}
}

// A rework target that exists in the snapshot but never produced a step row
// is unknown to the engine (nothing to re-enter) — refused atomically.
func TestRejectToStepLessNodeIsUnknown(t *testing.T) {
	f := newTestFixture(t)
	tmpl := f.createPublishedTemplate("freeze2", []NodeInput{
		agentNode("plan", RoleExecutor, "Executor Agent", NodeConfig{}),
		typedNode("freeze", NodeTypeAcceptance, NodeConfig{}),
		agentNode("impl", RoleExecutor, "Executor Agent", NodeConfig{}),
		typedNode("end", NodeTypeEnd, NodeConfig{}),
	}, linearEdges("plan", "freeze", "impl", "end"))
	run := f.startRun(tmpl, "ext-stepless", "Stepless reject target")
	ctx := context.Background()

	f.passExecutorStep(run.ID, "plan", nil)
	acc, err := f.queries.GetPendingAcceptanceByRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("pending acceptance: %v", err)
	}
	// impl is pre-created pending, but "end" has NO step row at all yet.
	if err := f.engine.RejectAcceptance(ctx, run.ID, acc.ID, util.MustParseUUID(f.memberID), "end", "redo"); !errors.Is(err, ErrReworkTargetUnknown) {
		t.Fatalf("reject to step-less node = %v, want ErrReworkTargetUnknown", err)
	}
	if got, err := f.queries.GetAcceptance(ctx, acc.ID); err != nil || got.Status != "pending" {
		t.Fatalf("acceptance = %q err=%v, want still pending (atomic refusal)", got.Status, err)
	}
}

// The hook-resolved reviewer (run context) takes precedence over the node
// config and is notified at activation. (A non-member reviewer cannot occur
// here: acceptance.reviewer_id FKs to member, so the hook resolves or 400s
// before the run starts.)
func TestAcceptanceReviewerFromRunContext(t *testing.T) {
	f := newTestFixture(t)
	tmpl := chainTemplate(f, "chain")
	ctx := context.Background()

	run, created, err := f.engine.StartRun(ctx, StartRunParams{
		WorkspaceID: util.MustParseUUID(f.workspaceID),
		TemplateID:  tmpl.Template.ID,
		SourceType:  "hook",
		SourceID:    "ext-rev-ctx",
		Title:       "Reviewer from context",
		InitiatorID: util.MustParseUUID(f.userID),
		ReviewerID:  util.MustParseUUID(f.memberID),
	})
	if err != nil || !created {
		t.Fatalf("start run: %v created=%v", err, created)
	}
	acc := driveToAcceptance(f, run)
	if acc.ReviewerID != util.MustParseUUID(f.memberID) {
		t.Fatalf("reviewer = %v, want run-context member %s", acc.ReviewerID, f.memberID)
	}
	var n int
	if err := f.pool.QueryRow(ctx, `
		SELECT count(*) FROM inbox_item
		WHERE workspace_id = $1 AND type = 'workflow_acceptance' AND recipient_id = $2
	`, f.workspaceID, f.userID).Scan(&n); err != nil {
		t.Fatalf("count inbox: %v", err)
	}
	if n != 1 {
		t.Fatalf("reviewer inbox = %d, want 1", n)
	}
}

// A node whose snapshot type the P0 engine cannot dispatch surfaces the
// activation error without parking the step (the P1 sweeper reconciles).
func TestActivateNodeUnsupportedType(t *testing.T) {
	f := newTestFixture(t)
	tmpl := f.createPublishedTemplate("badtype", []NodeInput{
		agentNode("plan", RoleExecutor, "Executor Agent", NodeConfig{}),
		agentNode("work", RoleExecutor, "Executor Agent", NodeConfig{}),
		typedNode("end", NodeTypeEnd, NodeConfig{}),
	}, linearEdges("plan", "work", "end"))
	run := f.startRun(tmpl, "ext-badtype", "Unsupported node type")
	ctx := context.Background()

	// Rewrite the second node's snapshot type to a P1-only type.
	var snapRaw []byte
	if err := f.pool.QueryRow(ctx, `SELECT template_snapshot FROM workflow_run WHERE id = $1`, run.ID).Scan(&snapRaw); err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	snap, err := ParseSnapshot(snapRaw)
	if err != nil {
		t.Fatalf("parse snapshot: %v", err)
	}
	for i := range snap.Nodes {
		if snap.Nodes[i].NodeKey == "work" {
			snap.Nodes[i].Type = "gate"
		}
	}
	patched, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE workflow_run SET template_snapshot = $2 WHERE id = $1`, run.ID, patched); err != nil {
		t.Fatalf("patch snapshot: %v", err)
	}

	f.passExecutorStep(run.ID, "plan", nil)

	// The step activated in-tx but could not dispatch: still active, run
	// still running (no failActivation parking for type errors).
	work := f.latestStep(run.ID, "work")
	if work.Status != StepActive {
		t.Fatalf("work = %q, want active (undispatchable type surfaces, not parks)", work.Status)
	}
	if got := f.runStatus(run.ID); got != RunRunning {
		t.Fatalf("run = %q, want running", got)
	}
}

// Exit-field values that cannot marshal (transport layers never produce
// these, but the engine validates defensively) fail before any write.
func TestRecordSubmissionUnmarshalableExitFields(t *testing.T) {
	f := newTestFixture(t)
	tmpl := chainTemplate(f, "chain")
	run := f.startRun(tmpl, "ext-badjson", "Unmarshalable exit fields")

	plan := f.latestStep(run.ID, "plan")
	_, _, err := f.engine.RecordSubmission(context.Background(), plan.AgentTaskID, SubmissionInput{
		Status: SubmissionDone,
		// spec_url satisfies the schema; the unknown bogus field is tolerated
		// by validation (D-9) but cannot be marshalled.
		ExitFields: map[string]any{"spec_url": "https://spec", "bogus": func() {}},
	})
	if err == nil || !strings.Contains(err.Error(), "marshal exit fields") {
		t.Fatalf("unmarshalable exit fields = %v, want marshal error", err)
	}
	if _, err := f.queries.GetSubmissionByStepInstance(context.Background(), plan.ID); err == nil {
		t.Fatalf("failed submission must not persist")
	}
}
