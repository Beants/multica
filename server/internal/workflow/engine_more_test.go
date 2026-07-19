package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// engine_more_test.go — second-tier engine coverage: activation failure
// landing, engine-level submission semantics, step-context assembly, the
// acceptance reviewer notification path, and template lifecycle/read paths.

func TestActivationFailureBlocksStepAndPauses(t *testing.T) {
	f := newTestFixture(t)
	tmpl := f.createPublishedTemplate("no-runtime", []NodeInput{
		agentNode("work", RoleExecutor, "Executor Agent", NodeConfig{}),
		typedNode("end", NodeTypeEnd, NodeConfig{}),
	}, linearEdges("work", "end"))

	// Archive the node's frozen agent AFTER publish: the enqueue refuses
	// archived agents, so activation must land as blocked + paused, not a
	// silent stall.
	if _, err := f.pool.Exec(context.Background(),
		`UPDATE agent SET archived_at = now() WHERE id = $1`, f.executorID); err != nil {
		t.Fatalf("archive agent: %v", err)
	}

	run, created, err := f.engine.StartRun(context.Background(), StartRunParams{
		WorkspaceID: util.MustParseUUID(f.workspaceID),
		TemplateID:  tmpl.Template.ID,
		SourceType:  "manual",
		Title:       "Doomed run",
		InitiatorID: util.MustParseUUID(f.userID),
	})
	if err == nil {
		t.Fatalf("start run should surface the dispatch failure")
	}
	if !created {
		t.Fatalf("run row exists even though dispatch failed (created=%v)", created)
	}

	work := f.latestStep(run.ID, "work")
	if work.Status != StepBlocked {
		t.Fatalf("work = %q, want blocked (dispatch failure landing)", work.Status)
	}
	if got := f.runStatus(run.ID); got != RunPaused {
		t.Fatalf("run = %q, want paused", got)
	}
	if n := f.inboxCount("workflow_blocked"); n != 1 {
		t.Fatalf("blocked inbox = %d, want 1", n)
	}
}

func TestRecordSubmissionEngineSemantics(t *testing.T) {
	f := newTestFixture(t)
	tmpl := f.createPublishedTemplate("sub-sem", []NodeInput{
		agentNode("work", RoleExecutor, "Executor Agent", NodeConfig{}),
		agentNode("gate", RoleEvaluator, "Evaluator Agent", NodeConfig{}),
		typedNode("end", NodeTypeEnd, NodeConfig{}),
	}, linearEdges("work", "gate", "end"))
	run := f.startRun(tmpl, "ext-subsem", "Submission semantics")
	ctx := context.Background()

	work := f.latestStep(run.ID, "work")
	// Unknown status is rejected before any write.
	if _, _, err := f.engine.RecordSubmission(ctx, work.AgentTaskID, SubmissionInput{Status: "PARTIAL"}); err == nil {
		t.Fatalf("unknown status must be rejected")
	}

	sub, created, err := f.engine.RecordSubmission(ctx, work.AgentTaskID, SubmissionInput{
		Status: SubmissionDone, IdempotencyKey: "k1",
	})
	if err != nil || !created {
		t.Fatalf("first submission: %v created=%v", err, created)
	}
	// Replay by key: same row, created=false, no double advance.
	replay, created, err := f.engine.RecordSubmission(ctx, work.AgentTaskID, SubmissionInput{
		Status: SubmissionDone, IdempotencyKey: "k1",
	})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if created || replay.ID != sub.ID {
		t.Fatalf("replay = %v created=%v, want %v/false", replay.ID, created, sub.ID)
	}
	// A distinct key on the same step is refused: the k1 DONE submission
	// derived a pass verdict, so the step is already terminal and the
	// terminal-step guard (Gate-W1 follow-up) fires before the
	// UNIQUE(step_instance_id) conflict would.
	if _, _, err := f.engine.RecordSubmission(ctx, work.AgentTaskID, SubmissionInput{
		Status: SubmissionDone, IdempotencyKey: "k2",
	}); !errors.Is(err, ErrStepTerminal) {
		t.Fatalf("distinct key on terminal step = %v, want ErrStepTerminal", err)
	}

	// Evaluator step: a direct submission derives NO system verdict — the
	// verdict actor model keeps executor/evaluator paths apart.
	gate := f.latestStep(run.ID, "gate")
	if gate.Status != StepActive {
		t.Fatalf("gate = %q, want active", gate.Status)
	}
	if _, _, err := f.engine.RecordSubmission(ctx, gate.AgentTaskID, SubmissionInput{Status: SubmissionDone}); err != nil {
		t.Fatalf("evaluator submission: %v", err)
	}
	if _, err := f.queries.GetVerdictByStepInstance(ctx, gate.ID); err == nil {
		t.Fatalf("evaluator submission must not derive a system verdict")
	}
	// The step waits for the evaluator's verdict instead of advancing.
	if got := f.latestStep(run.ID, "gate").Status; got != StepActive {
		t.Fatalf("gate = %q, want still active awaiting verdict", got)
	}

	// Verdict with confidence exercises the NUMERIC write path.
	conf := 0.75
	if _, err := f.engine.RecordVerdict(ctx, gate.AgentTaskID, VerdictInput{
		Result: VerdictPass, Confidence: &conf,
	}); err != nil {
		t.Fatalf("record verdict: %v", err)
	}
	if got := f.runStatus(run.ID); got != RunCompleted {
		t.Fatalf("run = %q, want completed", got)
	}
}

func TestGetStepContextEngineLevel(t *testing.T) {
	f := newTestFixture(t)
	tmpl := chainTemplate(f, "chain")
	run := f.startRun(tmpl, "ext-ctx", "Context")
	ctx := context.Background()

	f.passExecutorStep(run.ID, "plan", map[string]any{"spec_url": "https://spec"})
	impl := f.latestStep(run.ID, "implement")

	sc, err := f.engine.GetStepContext(ctx, impl.AgentTaskID)
	if err != nil {
		t.Fatalf("get step context: %v", err)
	}
	if sc.NodeKey != "implement" || sc.Role != RoleExecutor || sc.Attempt != 1 {
		t.Fatalf("sc = %+v", sc)
	}
	if sc.Instructions != "Build it" {
		t.Fatalf("instructions = %q", sc.Instructions)
	}
	if sc.UpstreamNodeKey != "plan" || sc.UpstreamExitFields["spec_url"] != "https://spec" {
		t.Fatalf("upstream = %q %+v", sc.UpstreamNodeKey, sc.UpstreamExitFields)
	}

	// Unknown task → ErrStepNotFound.
	if _, err := f.engine.GetStepContext(ctx, util.MustParseUUID("00000000-0000-0000-0000-000000000099")); !errors.Is(err, ErrStepNotFound) {
		t.Fatalf("unknown task = %v, want ErrStepNotFound", err)
	}
}

func TestAcceptanceReviewerNotified(t *testing.T) {
	f := newTestFixture(t)
	tmpl := f.createPublishedTemplate("reviewer", []NodeInput{
		agentNode("work", RoleExecutor, "Executor Agent", NodeConfig{}),
		typedNode("accept", NodeTypeAcceptance, NodeConfig{ReviewerID: f.memberID}),
		typedNode("end", NodeTypeEnd, NodeConfig{}),
	}, linearEdges("work", "accept", "end"))
	run := f.startRun(tmpl, "ext-reviewer", "Reviewer")

	f.passExecutorStep(run.ID, "work", nil)
	if got := f.runStatus(run.ID); got != RunWaitingAcceptance {
		t.Fatalf("run = %q, want waiting_acceptance", got)
	}
	acc, err := f.queries.GetPendingAcceptanceByRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("pending acceptance: %v", err)
	}
	if acc.ReviewerID != util.MustParseUUID(f.memberID) {
		t.Fatalf("reviewer = %v, want member %s", acc.ReviewerID, f.memberID)
	}
	// The reviewer got an actionable inbox pointing at the acceptance.
	var n int
	if err := f.pool.QueryRow(context.Background(), `
		SELECT count(*) FROM inbox_item
		WHERE workspace_id = $1 AND type = 'workflow_acceptance' AND recipient_id = $2
	`, f.workspaceID, f.userID).Scan(&n); err != nil {
		t.Fatalf("count inbox: %v", err)
	}
	if n != 1 {
		t.Fatalf("reviewer inbox = %d, want 1", n)
	}
}

func TestTemplateLifecycle(t *testing.T) {
	f := newTestFixture(t)
	ctx := context.Background()
	wsID := util.MustParseUUID(f.workspaceID)

	draft := f.createDraft(ctx, "lifecycle")

	// Get + List.
	if _, err := f.templates.GetTemplate(ctx, wsID, draft.Template.ID); err != nil {
		t.Fatalf("get: %v", err)
	}
	list, err := f.templates.ListTemplates(ctx, wsID)
	if err != nil || len(list) == 0 {
		t.Fatalf("list = %d, err=%v", len(list), err)
	}
	if _, err := f.templates.GetTemplate(ctx, wsID, util.MustParseUUID("00000000-0000-0000-0000-000000000099")); !errors.Is(err, ErrTemplateNotFound) {
		t.Fatalf("get unknown = %v, want ErrTemplateNotFound", err)
	}

	// Update draft: rename + replace the graph.
	newName := "lifecycle v2"
	updated, err := f.templates.UpdateTemplate(ctx, UpdateTemplateParams{
		WorkspaceID: wsID,
		TemplateID:  draft.Template.ID,
		Name:        &newName,
		Nodes: []NodeInput{
			agentNode("solo", RoleExecutor, "Executor Agent", NodeConfig{}),
		},
		ReplaceGraph: true,
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Template.Name != newName || len(updated.Nodes) != 1 || updated.Nodes[0].NodeKey != "solo" {
		t.Fatalf("updated = %q nodes=%v", updated.Template.Name, updated.Nodes)
	}

	// Archive: draft → archived; a second archive is a guarded conflict.
	archived, err := f.templates.ArchiveTemplate(ctx, wsID, draft.Template.ID)
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	if archived.Status != "archived" {
		t.Fatalf("status = %q", archived.Status)
	}
	if _, err := f.templates.ArchiveTemplate(ctx, wsID, draft.Template.ID); !errors.Is(err, ErrTemplateConflict) {
		t.Fatalf("re-archive = %v, want ErrTemplateConflict", err)
	}
	// Archived templates cannot be versioned.
	if _, err := f.templates.CreateTemplateVersion(ctx, wsID, draft.Template.ID, "", util.MustParseUUID(f.userID)); err == nil {
		t.Fatalf("archived template must not fork")
	}
	// Archived templates cannot start runs.
	if _, _, err := f.engine.StartRun(ctx, StartRunParams{
		WorkspaceID: wsID, TemplateID: draft.Template.ID,
		SourceType: "manual", Title: "x",
	}); !errors.Is(err, ErrTemplateNotPublished) {
		t.Fatalf("start on archived = %v, want ErrTemplateNotPublished", err)
	}
}

func TestResolveAgentVariants(t *testing.T) {
	f := newTestFixture(t)
	ctx := context.Background()
	wsID := util.MustParseUUID(f.workspaceID)

	// UUID selector resolves directly (no name lookup).
	byID, err := f.templates.CreateTemplate(ctx, CreateTemplateParams{
		WorkspaceID: wsID,
		Key:         "by-uuid",
		Name:        "by-uuid",
		CreatedBy:   util.MustParseUUID(f.userID),
		Nodes: []NodeInput{
			agentNode("work", RoleExecutor, f.executorID, NodeConfig{}),
			typedNode("end", NodeTypeEnd, NodeConfig{}),
		},
		Edges: linearEdges("work", "end"),
	})
	if err != nil {
		t.Fatalf("create by-uuid: %v", err)
	}
	if _, err := f.templates.PublishTemplate(ctx, wsID, byID.Template.ID); err != nil {
		t.Fatalf("publish by-uuid: %v", err)
	}

	// Unknown name → ErrAgentNotFound.
	unknown := f.createDraftWithSelector(ctx, "unknown-agent", "Nobody Here")
	if _, err := f.templates.PublishTemplate(ctx, wsID, unknown.Template.ID); !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("unknown agent = %v, want ErrAgentNotFound", err)
	}

	// Archived agents are invisible to selector resolution. (Name ambiguity
	// is unreachable: agent names are UNIQUE per workspace.)
	archived := f.createDraftWithSelector(ctx, "archived-agent", "Executor Agent")
	if _, err := f.pool.Exec(ctx, `UPDATE agent SET archived_at = now() WHERE id = $1`, f.executorID); err != nil {
		t.Fatalf("archive agent: %v", err)
	}
	if _, err := f.templates.PublishTemplate(ctx, wsID, archived.Template.ID); !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("archived agent = %v, want ErrAgentNotFound", err)
	}
}

// createDraftWithSelector builds a one-node draft whose selector is an
// arbitrary string (resolution outcome exercised at publish).
func (f *testFixture) createDraftWithSelector(ctx context.Context, key, selector string) *TemplateDetail {
	f.t.Helper()
	detail, err := f.templates.CreateTemplate(ctx, CreateTemplateParams{
		WorkspaceID: util.MustParseUUID(f.workspaceID),
		Key:         key,
		Name:        key,
		CreatedBy:   util.MustParseUUID(f.userID),
		Nodes: []NodeInput{
			agentNode("work", RoleExecutor, selector, NodeConfig{}),
			typedNode("end", NodeTypeEnd, NodeConfig{}),
		},
		Edges: linearEdges("work", "end"),
	})
	if err != nil {
		f.t.Fatalf("create draft %s: %v", key, err)
	}
	return detail
}

func TestValidateExitFieldsTypeCoverage(t *testing.T) {
	t.Parallel()
	schema := &ExitFieldsSchema{Fields: []ExitFieldSpec{
		{Name: "s", Type: "string", Required: true},
		{Name: "n", Type: "number"},
		{Name: "b", Type: "boolean"},
		{Name: "o", Type: "object"},
		{Name: "a", Type: "array"},
		{Name: "any1", Type: "any"},
		{Name: "implicit", Type: ""},
	}}
	good := map[string]any{
		"s": "text", "n": 1.5, "b": true, "o": map[string]any{"k": "v"},
		"a": []any{1, 2}, "any1": []any{}, "implicit": nil,
	}
	if errs := ValidateExitFields(schema, good); len(errs) != 0 {
		t.Fatalf("good payload rejected: %+v", errs)
	}
	bad := map[string]any{
		"s": 1, "n": "x", "b": "yes", "o": []any{}, "a": map[string]any{},
	}
	errs := ValidateExitFields(schema, bad)
	// s/n/b/o/a all mismatch (implicit+any accept anything).
	if len(errs) != 5 {
		t.Fatalf("expected 5 type mismatches, got %+v", errs)
	}
	for _, e := range errs {
		if e.Code != "type_mismatch" {
			t.Fatalf("error = %+v, want type_mismatch", e)
		}
	}

	// Status-aware: BLOCKED skips missing-required, DONE does not.
	if errs := ValidateExitFieldsForStatus(SubmissionBlocked, schema, map[string]any{}); len(errs) != 0 {
		t.Fatalf("blocked must skip required checks, got %+v", errs)
	}
	if errs := ValidateExitFieldsForStatus(SubmissionDone, schema, map[string]any{}); len(errs) != 1 || errs[0].Code != "missing" {
		t.Fatalf("done must require fields, got %+v", errs)
	}
	if errs := ValidateExitFieldsForStatus(SubmissionNeedsContext, schema, map[string]any{"s": "x", "n": "bad"}); len(errs) != 1 || errs[0].Name != "n" {
		t.Fatalf("needs_context keeps type checks, got %+v", errs)
	}
}

// ---------------------------------------------------------------------------
// Error-path and edge coverage
// ---------------------------------------------------------------------------

func TestStartRunValidation(t *testing.T) {
	f := newTestFixture(t)
	ctx := context.Background()
	tmpl := chainTemplate(f, "chain")

	if _, _, err := f.engine.StartRun(ctx, StartRunParams{
		WorkspaceID: util.MustParseUUID(f.workspaceID), TemplateID: tmpl.Template.ID,
		SourceType: "hook", Title: "",
	}); err == nil {
		t.Fatalf("empty title must be rejected")
	}
	if _, _, err := f.engine.StartRun(ctx, StartRunParams{
		WorkspaceID: util.MustParseUUID(f.workspaceID), TemplateID: tmpl.Template.ID,
		SourceType: "pigeon", Title: "x",
	}); err == nil {
		t.Fatalf("unknown source_type must be rejected")
	}
	if _, _, err := f.engine.StartRun(ctx, StartRunParams{
		WorkspaceID: util.MustParseUUID(f.workspaceID),
		TemplateID:  util.MustParseUUID("00000000-0000-0000-0000-000000000099"),
		SourceType:  "hook", Title: "x",
	}); !errors.Is(err, ErrTemplateNotFound) {
		t.Fatalf("unknown template = %v, want ErrTemplateNotFound", err)
	}
	// Draft (unpublished) templates cannot start runs.
	draft := f.createDraft(ctx, "draft-run")
	if _, _, err := f.engine.StartRun(ctx, StartRunParams{
		WorkspaceID: util.MustParseUUID(f.workspaceID), TemplateID: draft.Template.ID,
		SourceType: "hook", Title: "x",
	}); !errors.Is(err, ErrTemplateNotPublished) {
		t.Fatalf("draft template = %v, want ErrTemplateNotPublished", err)
	}
}

func TestParseNodeConfigValidation(t *testing.T) {
	t.Parallel()
	if _, err := ParseNodeConfig([]byte(`{"role": "wizard"}`)); err == nil {
		t.Fatalf("unknown role must be rejected")
	}
	if _, err := ParseNodeConfig([]byte(`{"exit_fields": {"fields": [{"name": "", "type": "string"}]}}`)); err == nil {
		t.Fatalf("empty field name must be rejected")
	}
	if _, err := ParseNodeConfig([]byte(`{"exit_fields": {"fields": [{"name": "x", "type": "date"}]}}`)); err == nil {
		t.Fatalf("unknown field type must be rejected")
	}
	if _, err := ParseNodeConfig([]byte(`{not json`)); err == nil {
		t.Fatalf("malformed JSON must be rejected")
	}
	cfg, err := ParseNodeConfig(nil)
	if err != nil || cfg.EffectiveRole() != RoleExecutor || cfg.EffectiveMaxAttempts() != defaultMaxAttempts {
		t.Fatalf("zero config defaults: %+v err=%v", cfg, err)
	}
	// Unknown fields tolerated (forward-compat D-9).
	cfg, err = ParseNodeConfig([]byte(`{"role": "evaluator", "future_knob": true}`))
	if err != nil || cfg.EffectiveRole() != RoleEvaluator {
		t.Fatalf("unknown fields must pass through: %+v err=%v", cfg, err)
	}
	if got := (&ExitFieldsValidationError{Fields: []FieldError{{Name: "x"}}}).Error(); got == "" {
		t.Fatalf("validation error string empty")
	}
	if got := (&EvaluatorSeparationError{NodeKey: "g", UpstreamKey: "w", AgentID: "a"}).Error(); got == "" {
		t.Fatalf("separation error string empty")
	}
}

func TestRecordVerdictEngineEdges(t *testing.T) {
	f := newTestFixture(t)
	tmpl := chainTemplate(f, "chain")
	run := f.startRun(tmpl, "ext-vedges", "Verdict edges")
	ctx := context.Background()

	// Executor step → ErrNotEvaluatorStep (verdict actor model).
	plan := f.latestStep(run.ID, "plan")
	if _, err := f.engine.RecordVerdict(ctx, plan.AgentTaskID, VerdictInput{Result: VerdictPass}); !errors.Is(err, ErrNotEvaluatorStep) {
		t.Fatalf("executor verdict = %v, want ErrNotEvaluatorStep", err)
	}
	if _, err := f.engine.RecordVerdict(ctx, plan.AgentTaskID, VerdictInput{Result: "shrug"}); err == nil {
		t.Fatalf("unknown result must be rejected")
	}

	// Evaluator verdict without a submission: auto-create passes the same
	// schema validation — plan's schema does not apply to review, which has
	// no required fields here, so a bare verdict succeeds.
	f.passExecutorStep(run.ID, "plan", map[string]any{"spec_url": "https://spec"})
	f.passExecutorStep(run.ID, "implement", nil)
	review := f.latestStep(run.ID, "review")
	v, err := f.engine.RecordVerdict(ctx, review.AgentTaskID, VerdictInput{
		Result: VerdictFail, RootCause: "no tests",
	})
	if err != nil {
		t.Fatalf("record verdict: %v", err)
	}
	if v.VerdictBy != "agent" {
		t.Fatalf("verdict_by = %q, want agent", v.VerdictBy)
	}
	// A second verdict against the SAME (failed) attempt is refused by the
	// terminal-step guard (Gate-W1 follow-up): the fail verdict already
	// terminated attempt 1 and spun up a retry, so the old task's step no
	// longer accepts writes.
	if _, err := f.engine.RecordVerdict(ctx, review.AgentTaskID, VerdictInput{Result: VerdictPass}); !errors.Is(err, ErrStepTerminal) {
		t.Fatalf("second verdict on terminal step = %v, want ErrStepTerminal", err)
	}
}

func TestRecordSubmissionTypeMismatch(t *testing.T) {
	f := newTestFixture(t)
	tmpl := chainTemplate(f, "chain")
	run := f.startRun(tmpl, "ext-mismatch", "Mismatch")
	plan := f.latestStep(run.ID, "plan")

	var validationErr *ExitFieldsValidationError
	_, _, err := f.engine.RecordSubmission(context.Background(), plan.AgentTaskID, SubmissionInput{
		Status:     SubmissionDone,
		ExitFields: map[string]any{"spec_url": 42},
	})
	if !errors.As(err, &validationErr) {
		t.Fatalf("type mismatch = %v, want ExitFieldsValidationError", err)
	}
	if len(validationErr.Fields) != 1 || validationErr.Fields[0].Code != "type_mismatch" {
		t.Fatalf("fields = %+v, want one type_mismatch", validationErr.Fields)
	}
}

func TestRecordSubmissionRejectsLocalPathArtifacts(t *testing.T) {
	f := newTestFixture(t)
	tmpl := chainTemplate(f, "chain")
	run := f.startRun(tmpl, "ext-artifacts", "Artifacts D-11")
	ctx := context.Background()
	plan := f.latestStep(run.ID, "plan")

	// Layer-2 D-11: a workdir path in artifacts is rejected before any write.
	var artifactsErr *ArtifactsValidationError
	_, _, err := f.engine.RecordSubmission(ctx, plan.AgentTaskID, SubmissionInput{
		Status:     SubmissionDone,
		ExitFields: map[string]any{"spec_url": "https://spec.example/1"},
		Artifacts:  json.RawMessage(`{"report": "multica_workspaces/ws/task/workdir/report.md"}`),
	})
	if !errors.As(err, &artifactsErr) {
		t.Fatalf("workdir artifact = %v, want ArtifactsValidationError", err)
	}
	if len(artifactsErr.Fields) != 1 || artifactsErr.Fields[0].Code != "local_path" {
		t.Fatalf("fields = %+v, want one local_path", artifactsErr.Fields)
	}
	if _, qerr := f.queries.GetSubmissionByStepInstance(ctx, plan.ID); !errors.Is(qerr, pgx.ErrNoRows) {
		t.Fatalf("rejected submission must not land, got %v", qerr)
	}

	// Durable references (PR URL + branch) pass the same check.
	if _, created, err := f.engine.RecordSubmission(ctx, plan.AgentTaskID, SubmissionInput{
		Status:     SubmissionDone,
		ExitFields: map[string]any{"spec_url": "https://spec.example/1"},
		Artifacts:  json.RawMessage(`{"pr_url": "https://github.com/org/repo/pull/7", "branch": "feat/x"}`),
	}); err != nil || !created {
		t.Fatalf("durable artifacts must pass: %v created=%v", err, created)
	}
}

func TestAcceptanceDecisionValidation(t *testing.T) {
	f := newTestFixture(t)
	tmpl := chainTemplate(f, "chain")
	run := f.startRun(tmpl, "ext-accval", "Acceptance validation")
	ctx := context.Background()
	acc := driveToAcceptance(f, run)

	if err := f.engine.RejectAcceptance(ctx, run.ID, acc.ID, util.MustParseUUID(f.memberID), "", "reason"); err == nil {
		t.Fatalf("empty target must be rejected")
	}
	if err := f.engine.RejectAcceptance(ctx, run.ID, acc.ID, util.MustParseUUID(f.memberID), "implement", ""); err == nil {
		t.Fatalf("empty reason must be rejected")
	}
	if err := f.engine.RejectAcceptance(ctx, run.ID, acc.ID, util.MustParseUUID(f.memberID), "nonexistent-node", "reason"); !errors.Is(err, ErrReworkTargetUnknown) {
		t.Fatalf("unknown target = %v, want ErrReworkTargetUnknown", err)
	}
	// Unknown run → ErrRunNotFound.
	if err := f.engine.RejectAcceptance(ctx, util.MustParseUUID("00000000-0000-0000-0000-000000000099"), acc.ID, util.MustParseUUID(f.memberID), "implement", "r"); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("unknown run = %v, want ErrRunNotFound", err)
	}
	// Rework on a paused run → ErrRunNotActive.
	if _, err := f.queries.UpdateWorkflowRunStatus(ctx, db.UpdateWorkflowRunStatusParams{
		NewStatus: RunPaused, ID: run.ID, ExpectedStatus: RunWaitingAcceptance,
	}); err != nil {
		t.Fatalf("pause run: %v", err)
	}
	if err := f.engine.RequestRework(ctx, run.ID, "implement", nil); !errors.Is(err, ErrRunNotActive) {
		t.Fatalf("rework on paused run = %v, want ErrRunNotActive", err)
	}
}

func TestSignalVerdictWithoutVerdict(t *testing.T) {
	f := newTestFixture(t)
	tmpl := chainTemplate(f, "chain")
	run := f.startRun(tmpl, "ext-noverdict", "No verdict")

	plan := f.latestStep(run.ID, "plan")
	if err := f.engine.SignalVerdict(context.Background(), plan.ID); !errors.Is(err, ErrVerdictNotFound) {
		t.Fatalf("signal without verdict = %v, want ErrVerdictNotFound", err)
	}
	if err := f.engine.SignalVerdict(context.Background(), util.MustParseUUID("00000000-0000-0000-0000-000000000099")); !errors.Is(err, ErrStepNotFound) {
		t.Fatalf("signal unknown step = %v, want ErrStepNotFound", err)
	}
}

func TestDeriveSystemVerdictFallbacks(t *testing.T) {
	f := newTestFixture(t)
	tmpl := chainTemplate(f, "chain")
	ctx := context.Background()

	// BLOCKED without a raw summary → fallback root cause.
	run := f.startRun(tmpl, "ext-fallback", "Fallback")
	plan := f.latestStep(run.ID, "plan")
	if _, _, err := f.engine.RecordSubmission(ctx, plan.AgentTaskID, SubmissionInput{Status: SubmissionBlocked}); err != nil {
		t.Fatalf("blocked submission: %v", err)
	}
	verdict, err := f.queries.GetVerdictByStepInstance(ctx, plan.ID)
	if err != nil {
		t.Fatalf("verdict: %v", err)
	}
	if verdict.Result != VerdictBlocked || verdict.RootCause.String != "agent reported BLOCKED" {
		t.Fatalf("verdict = %q cause %q", verdict.Result, verdict.RootCause.String)
	}

	// NEEDS_CONTEXT maps to blocked too.
	run2 := f.startRun(tmpl, "ext-fallback2", "Fallback 2")
	plan2 := f.latestStep(run2.ID, "plan")
	if _, _, err := f.engine.RecordSubmission(ctx, plan2.AgentTaskID, SubmissionInput{Status: SubmissionNeedsContext}); err != nil {
		t.Fatalf("needs_context submission: %v", err)
	}
	verdict2, err := f.queries.GetVerdictByStepInstance(ctx, plan2.ID)
	if err != nil {
		t.Fatalf("verdict2: %v", err)
	}
	if verdict2.Result != VerdictBlocked || verdict2.RootCause.String != "agent reported NEEDS_CONTEXT" {
		t.Fatalf("verdict2 = %q cause %q", verdict2.Result, verdict2.RootCause.String)
	}

	// DONE_WITH_CONCERNS with no gaps still passes with an evidence shell.
	run3 := f.startRun(tmpl, "ext-fallback3", "Fallback 3")
	plan3 := f.latestStep(run3.ID, "plan")
	if _, _, err := f.engine.RecordSubmission(ctx, plan3.AgentTaskID, SubmissionInput{
		Status:     SubmissionDoneWithConcerns,
		ExitFields: map[string]any{"spec_url": "https://spec"},
	}); err != nil {
		t.Fatalf("concerns submission: %v", err)
	}
	verdict3, err := f.queries.GetVerdictByStepInstance(ctx, plan3.ID)
	if err != nil {
		t.Fatalf("verdict3: %v", err)
	}
	if verdict3.Result != VerdictPass || !strings.Contains(string(verdict3.Evidence), "concerns") {
		t.Fatalf("verdict3 = %q evidence %s", verdict3.Result, verdict3.Evidence)
	}
}

func TestMidChainAcceptanceAdvances(t *testing.T) {
	f := newTestFixture(t)
	// Two acceptance nodes: approving the FIRST must activate the second
	// (advance-into-acceptance), and approving the second completes.
	tmpl := f.createPublishedTemplate("two-accept", []NodeInput{
		agentNode("work", RoleExecutor, "Executor Agent", NodeConfig{}),
		typedNode("freeze", NodeTypeAcceptance, NodeConfig{}),
		typedNode("final", NodeTypeAcceptance, NodeConfig{}),
		typedNode("end", NodeTypeEnd, NodeConfig{}),
	}, linearEdges("work", "freeze", "final", "end"))
	run := f.startRun(tmpl, "ext-twoacc", "Two acceptances")
	ctx := context.Background()

	f.passExecutorStep(run.ID, "work", nil)
	if got := f.runStatus(run.ID); got != RunWaitingAcceptance {
		t.Fatalf("run = %q, want waiting_acceptance (freeze)", got)
	}
	freeze := f.latestStep(run.ID, "freeze")
	acc, err := f.queries.GetPendingAcceptanceByRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("pending acceptance: %v", err)
	}
	if acc.StepInstanceID != freeze.ID {
		t.Fatalf("acceptance bound to %v, want freeze step %v", acc.StepInstanceID, freeze.ID)
	}
	if err := f.engine.ApproveAcceptance(ctx, run.ID, acc.ID, util.MustParseUUID(f.memberID)); err != nil {
		t.Fatalf("approve freeze: %v", err)
	}

	// The chain advanced into the SECOND acceptance wait.
	if got := f.runStatus(run.ID); got != RunWaitingAcceptance {
		t.Fatalf("run = %q, want waiting_acceptance (final)", got)
	}
	final := f.latestStep(run.ID, "final")
	if final.Status != StepActive {
		t.Fatalf("final = %q, want active", final.Status)
	}
	acc2, err := f.queries.GetPendingAcceptanceByRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("second pending acceptance: %v", err)
	}
	if acc2.StepInstanceID != final.ID {
		t.Fatalf("second acceptance bound to %v, want final step %v", acc2.StepInstanceID, final.ID)
	}
	// Both acceptances coexist (decided + pending — partial index OK).
	if err := f.engine.ApproveAcceptance(ctx, run.ID, acc2.ID, util.MustParseUUID(f.memberID)); err != nil {
		t.Fatalf("approve final: %v", err)
	}
	if got := f.runStatus(run.ID); got != RunCompleted {
		t.Fatalf("run = %q, want completed", got)
	}
}

func TestApproveAcceptanceNotWaiting(t *testing.T) {
	f := newTestFixture(t)
	tmpl := chainTemplate(f, "chain")
	run := f.startRun(tmpl, "ext-notwaiting", "Not waiting")
	ctx := context.Background()

	// The run is still running (no acceptance active): approving an
	// acceptance id that does not exist is a guarded conflict.
	if err := f.engine.ApproveAcceptance(ctx, run.ID, util.MustParseUUID("00000000-0000-0000-0000-000000000099"), util.MustParseUUID(f.memberID)); !errors.Is(err, ErrAcceptanceConflict) {
		t.Fatalf("approve unknown = %v, want ErrAcceptanceConflict", err)
	}
}

func TestRunWithoutInitiatorSkipsNotify(t *testing.T) {
	f := newTestFixture(t)
	tmpl := chainTemplate(f, "chain")
	// No initiator: notifications degrade to logs (creator falls back to the
	// zero-UUID system convention).
	run, created, err := f.engine.StartRun(context.Background(), StartRunParams{
		WorkspaceID: util.MustParseUUID(f.workspaceID),
		TemplateID:  tmpl.Template.ID,
		SourceType:  "autopilot",
		Title:       "System run",
	})
	if err != nil || !created {
		t.Fatalf("start run: %v created=%v", err, created)
	}
	plan := f.latestStep(run.ID, "plan")
	if _, _, err := f.engine.RecordSubmission(context.Background(), plan.AgentTaskID, SubmissionInput{Status: SubmissionBlocked}); err != nil {
		t.Fatalf("blocked submission: %v", err)
	}
	if got := f.runStatus(run.ID); got != RunPaused {
		t.Fatalf("run = %q, want paused", got)
	}
	if n := f.inboxCount("workflow_blocked"); n != 0 {
		t.Fatalf("inbox = %d, want 0 (no initiator to notify)", n)
	}
}

// TestHookRunNotifiesReviewerWithoutInitiator pins the AC1 负责人 path: a
// hook-originated run carries no initiator, so completion/escalation
// notifications and the human handoff must land with the hook-designated
// reviewer (member → user), not vanish into the "no initiator" log line.
func TestHookRunNotifiesReviewerWithoutInitiator(t *testing.T) {
	f := newTestFixture(t)
	tmpl := f.createPublishedTemplate("hook-reviewer", []NodeInput{
		agentNode("work", RoleExecutor, "Executor Agent", NodeConfig{}),
		typedNode("end", NodeTypeEnd, NodeConfig{}),
	}, linearEdges("work", "end"))

	startHookRun := func(sourceID string) db.WorkflowRun {
		run, created, err := f.engine.StartRun(context.Background(), StartRunParams{
			WorkspaceID: util.MustParseUUID(f.workspaceID),
			TemplateID:  tmpl.Template.ID,
			SourceType:  "hook",
			SourceID:    sourceID,
			Title:       "Hook run",
			ReviewerID:  util.MustParseUUID(f.memberID),
		})
		if err != nil || !created {
			t.Fatalf("start run: %v created=%v", err, created)
		}
		return run
	}
	recipientCount := func(typ string) int {
		var n int
		if err := f.pool.QueryRow(context.Background(),
			`SELECT count(*) FROM inbox_item WHERE workspace_id = $1 AND type = $2 AND recipient_id = $3`,
			f.workspaceID, typ, f.userID).Scan(&n); err != nil {
			t.Fatalf("count inbox: %v", err)
		}
		return n
	}

	// Completion: the reviewer's user gets the workflow_completed inbox.
	run := startHookRun("src-reviewer-complete")
	f.passExecutorStep(run.ID, "work", nil)
	if got := f.runStatus(run.ID); got != RunCompleted {
		t.Fatalf("run = %q, want completed", got)
	}
	if n := recipientCount("workflow_completed"); n != 1 {
		t.Fatalf("completion inbox for reviewer = %d, want 1 (AC1 负责人通知)", n)
	}

	// Escalation: a blocked submission pauses the run, inboxes the reviewer,
	// and reassigns the intake issue to the reviewer's user (转人工).
	run2 := startHookRun("src-reviewer-blocked")
	work := f.latestStep(run2.ID, "work")
	if _, _, err := f.engine.RecordSubmission(context.Background(), work.AgentTaskID, SubmissionInput{Status: SubmissionBlocked}); err != nil {
		t.Fatalf("blocked submission: %v", err)
	}
	if got := f.runStatus(run2.ID); got != RunPaused {
		t.Fatalf("run2 = %q, want paused", got)
	}
	if n := recipientCount("workflow_blocked"); n != 1 {
		t.Fatalf("blocked inbox for reviewer = %d, want 1", n)
	}
}

func TestTemplateCreateValidation(t *testing.T) {
	f := newTestFixture(t)
	ctx := context.Background()
	wsID := util.MustParseUUID(f.workspaceID)

	if _, err := f.templates.CreateTemplate(ctx, CreateTemplateParams{
		WorkspaceID: wsID, Key: "", Name: "x",
		Nodes: []NodeInput{agentNode("a", RoleExecutor, "Executor Agent", NodeConfig{})},
	}); err == nil {
		t.Fatalf("empty key must be rejected")
	}
	if _, err := f.templates.CreateTemplate(ctx, CreateTemplateParams{
		WorkspaceID: wsID, Key: "x", Name: "",
		Nodes: []NodeInput{agentNode("a", RoleExecutor, "Executor Agent", NodeConfig{})},
	}); err == nil {
		t.Fatalf("empty name must be rejected")
	}
	if _, err := f.templates.CreateTemplate(ctx, CreateTemplateParams{
		WorkspaceID: wsID, Key: "x", Name: "x",
		Nodes: []NodeInput{
			agentNode("a", RoleExecutor, "Executor Agent", NodeConfig{}),
			agentNode("b", RoleExecutor, "Executor Agent", NodeConfig{}),
		},
		// missing edge a→b: invalid graph
	}); err == nil {
		t.Fatalf("disconnected graph must be rejected")
	}
	// ReplaceGraph with an invalid graph fails before touching the draft.
	draft := f.createDraft(ctx, "update-invalid")
	if _, err := f.templates.UpdateTemplate(ctx, UpdateTemplateParams{
		WorkspaceID: wsID, TemplateID: draft.Template.ID,
		Nodes:        []NodeInput{agentNode("a", RoleExecutor, "Executor Agent", NodeConfig{}), agentNode("b", RoleExecutor, "Executor Agent", NodeConfig{})},
		ReplaceGraph: true,
	}); err == nil {
		t.Fatalf("invalid replace graph must be rejected")
	}
}
