package workflow

// Referential integrity: the workflow tables (migration 901) carry NO
// database-level foreign keys or cascading actions — repo hard rule
// (AGENTS.md / CLAUDE.md). Relationships are enforced here in the
// application layer: the engine writes parents before children inside one
// transaction (creation order: template -> run -> step_instance ->
// submission -> verdict, with acceptance/step_transition alongside their
// step), so a child row never commits without its parent.
//
// Known gap: workspace-delete cleanup of workflow rows is NOT implemented
// — it is a P1 integration point tracked in the task's follow-ups
// (previously ON DELETE CASCADE on workspace_id). Until then, deleting a
// workspace leaves orphaned workflow rows.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/issueposition"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// FlagEngine is the feature flag gating every workflow-engine surface
// (routes 404, listeners stay unsubscribed while off). Declared here rather
// than internal/featureflags/keys.go to keep the fork's upstream touch
// budget intact (design.md §5, §7).
const FlagEngine = "workflow_engine"

// Step statuses (step_instance.status CHECK).
const (
	StepPending    = "pending"
	StepActive     = "active"
	StepDispatched = "dispatched"
	StepRunning    = "running"
	StepPassed     = "passed"
	StepFailed     = "failed"
	StepBlocked    = "blocked"
	StepRework     = "rework"
	StepSkipped    = "skipped"
)

// Run statuses (workflow_run.status CHECK).
const (
	RunRunning           = "running"
	RunPaused            = "paused"
	RunCompleted         = "completed"
	RunFailed            = "failed"
	RunCancelled         = "cancelled"
	RunWaitingAcceptance = "waiting_acceptance"
)

// Submission statuses (harness four-state vocabulary, inventory D-2).
const (
	SubmissionDone             = "DONE"
	SubmissionDoneWithConcerns = "DONE_WITH_CONCERNS"
	SubmissionBlocked          = "BLOCKED"
	SubmissionNeedsContext     = "NEEDS_CONTEXT"
)

// Verdict results (verdict.result CHECK — naming discipline inventory 1.14:
// only pass/fail/blocked ever lives here).
const (
	VerdictPass    = "pass"
	VerdictFail    = "fail"
	VerdictBlocked = "blocked"
)

// Circuit-breaker thresholds (design.md §4.4): three consecutive reworks of
// one node, or three acceptance rejections across the run, pause the run
// and hand the intake issue to a human.
const circuitBreakerLimit = 3

// Sentinel errors mapped to HTTP codes by the handler layer.
var (
	ErrRunNotFound         = errors.New("workflow: run not found")
	ErrStepNotFound        = errors.New("workflow: step not found")
	ErrVerdictNotFound     = errors.New("workflow: verdict not found")
	ErrSubmissionExists    = errors.New("workflow: step already has a submission")
	ErrVerdictExists       = errors.New("workflow: submission already has a verdict")
	ErrNotEvaluatorStep    = errors.New("workflow: verdict writes require an evaluator-role step")
	ErrAcceptanceConflict  = errors.New("workflow: acceptance already decided")
	ErrAcceptanceNotFound  = errors.New("workflow: acceptance not found")
	ErrReworkTargetUnknown = errors.New("workflow: rework target node unknown or never ran")
	ErrReworkTargetActive  = errors.New("workflow: rework target has an in-flight step")
	ErrRunNotActive        = errors.New("workflow: run is not active")
	// ErrStepTerminal rejects submission/verdict writes on a terminal step
	// (Gate W1 follow-up): a skipped/failed old attempt must not accrete
	// noise rows whose signals the state machine ignores anyway.
	ErrStepTerminal = errors.New("workflow: step is terminal")
)

// isTerminalStepStatus reports whether a step ignores further signals —
// duplicate verdicts / late task outcomes land on terminal steps and must be
// no-ops (blueprint §8.1 推进幂等).
func isTerminalStepStatus(status string) bool {
	switch status {
	case StepPassed, StepFailed, StepBlocked, StepRework, StepSkipped:
		return true
	}
	return false
}

// Engine is the P0 linear workflow state machine (TS-1: StartRun /
// SignalVerdict / RequestRework, with edge evaluation collapsed to "default
// edge by priority" because P0 conditions are NULL). Concurrency follows
// blueprint §8: every status change is a guarded UPDATE with a
// step_transition row, verdict consumption re-reads the step under the run
// row lock, and hard UNIQUE constraints backstop every create.
type Engine struct {
	Queries   *db.Queries
	TxStarter TxStarter
	Issues    *service.IssueService
	Tasks     *service.TaskService
	Bus       *events.Bus
}

func NewEngine(q *db.Queries, tx TxStarter, issues *service.IssueService, tasks *service.TaskService, bus *events.Bus) *Engine {
	return &Engine{Queries: q, TxStarter: tx, Issues: issues, Tasks: tasks, Bus: bus}
}

// ---------------------------------------------------------------------------
// Run context (workflow_run.context JSONB)
// ---------------------------------------------------------------------------

// RunContext is the mutable run-level metadata. InitiatorID is the user UUID
// of the member who started the run (invalid when system-triggered); the
// circuit breaker hands the intake issue back to them. ReviewerID is the
// hook-resolved acceptance reviewer (member.id), falling back to the
// template node's reviewer_id.
type RunContext struct {
	InitiatorID string `json:"initiator_id,omitempty"`
	ReviewerID  string `json:"reviewer_id,omitempty"`
}

// ParseRunContext decodes run.context, tolerating unknown fields (D-9). A
// corrupt blob degrades to an empty context: the run row is still readable
// and notification paths treat a missing initiator as "nobody to ping".
func ParseRunContext(raw []byte) RunContext {
	var rc RunContext
	if len(raw) == 0 {
		return rc
	}
	if err := json.Unmarshal(raw, &rc); err != nil {
		return RunContext{}
	}
	return rc
}

func (rc RunContext) Initiator() pgtype.UUID {
	id, err := util.ParseUUID(rc.InitiatorID)
	if err != nil {
		return pgtype.UUID{}
	}
	return id
}

func (rc RunContext) Reviewer() pgtype.UUID {
	id, err := util.ParseUUID(rc.ReviewerID)
	if err != nil {
		return pgtype.UUID{}
	}
	return id
}

// ---------------------------------------------------------------------------
// StartRun
// ---------------------------------------------------------------------------

// StartRunParams carries one run start. SourceID is the EXTERNAL work
// identifier (hook payload's source_id; design.md §4.1) — the idempotency
// key; empty means a manual run that never dedupes. Title/Description fill
// the intake parent issue created in the same transaction.
type StartRunParams struct {
	WorkspaceID pgtype.UUID
	TemplateID  pgtype.UUID
	SourceType  string
	SourceID    string
	Title       string
	Description string
	InitiatorID pgtype.UUID
	ReviewerID  pgtype.UUID
}

// StartRun creates the intake parent issue + workflow_run + first two step
// rows (first node active, second pre-created pending — inventory D-3) in
// ONE transaction, then runs the start node's activation flow after commit.
// Returns (run, created, err): a duplicate push for the same
// (workspace, source_type, source_id, template) returns the existing run
// with created=false (blueprint §8.3).
func (e *Engine) StartRun(ctx context.Context, p StartRunParams) (db.WorkflowRun, bool, error) {
	if p.Title == "" {
		return db.WorkflowRun{}, false, errors.New("workflow: title is required")
	}
	switch p.SourceType {
	case "issue", "hook", "autopilot", "manual":
	default:
		return db.WorkflowRun{}, false, fmt.Errorf("workflow: unknown source_type %q", p.SourceType)
	}

	tmpl, err := e.Queries.GetWorkflowTemplateInWorkspace(ctx, db.GetWorkflowTemplateInWorkspaceParams{
		ID: p.TemplateID, WorkspaceID: p.WorkspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.WorkflowRun{}, false, ErrTemplateNotFound
		}
		return db.WorkflowRun{}, false, fmt.Errorf("get template: %w", err)
	}
	if tmpl.Status != "published" {
		return db.WorkflowRun{}, false, ErrTemplateNotPublished
	}
	snap, err := BuildSnapshot(ctx, e.Queries, tmpl)
	if err != nil {
		return db.WorkflowRun{}, false, err
	}
	startNode := snap.StartNode()
	if startNode == nil {
		return db.WorkflowRun{}, false, errors.New("workflow: template has no start node")
	}
	snapJSON, err := json.Marshal(snap)
	if err != nil {
		return db.WorkflowRun{}, false, fmt.Errorf("marshal snapshot: %w", err)
	}

	sourceID := pgtype.Text{String: p.SourceID, Valid: p.SourceID != ""}
	if sourceID.Valid {
		existing, err := e.Queries.GetWorkflowRunBySource(ctx, db.GetWorkflowRunBySourceParams{
			WorkspaceID: p.WorkspaceID, SourceType: p.SourceType, SourceID: sourceID, TemplateID: p.TemplateID,
		})
		if err == nil {
			return existing, false, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return db.WorkflowRun{}, false, fmt.Errorf("source idempotency check: %w", err)
		}
	}

	runCtx := RunContext{}
	if p.InitiatorID.Valid {
		runCtx.InitiatorID = util.UUIDToString(p.InitiatorID)
	}
	if p.ReviewerID.Valid {
		runCtx.ReviewerID = util.UUIDToString(p.ReviewerID)
	}
	runCtxJSON, err := json.Marshal(runCtx)
	if err != nil {
		return db.WorkflowRun{}, false, fmt.Errorf("marshal run context: %w", err)
	}

	tx, err := e.TxStarter.Begin(ctx)
	if err != nil {
		return db.WorkflowRun{}, false, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := e.Queries.WithTx(tx)

	// Issue numbering reuses the workspace counter (design.md §4.1): the
	// intake issue's number doubles as the run's human-readable sequence in
	// node child titles (<runNumber>-<node_key>-attempt<N>).
	issueNumber, err := qtx.IncrementIssueCounter(ctx, p.WorkspaceID)
	if err != nil {
		return db.WorkflowRun{}, false, fmt.Errorf("increment counter: %w", err)
	}
	position, err := issueposition.NextTopPosition(ctx, tx, p.WorkspaceID, "todo")
	if err != nil {
		return db.WorkflowRun{}, false, fmt.Errorf("next position: %w", err)
	}
	creatorID := p.InitiatorID
	if !creatorID.Valid {
		// issue.creator_id is NOT NULL; the zero UUID is the codebase's
		// system-actor convention (see postChildDoneComment).
		creatorID = pgtype.UUID{Valid: true}
	}
	intake, err := qtx.CreateIssue(ctx, db.CreateIssueParams{
		WorkspaceID: p.WorkspaceID,
		Title:       p.Title,
		Description: pgtype.Text{String: p.Description, Valid: p.Description != ""},
		Status:      "todo",
		Priority:    "none",
		CreatorType: "member",
		CreatorID:   creatorID,
		Position:    position,
		Number:      issueNumber,
	})
	if err != nil {
		return db.WorkflowRun{}, false, fmt.Errorf("create intake issue: %w", err)
	}
	run, err := qtx.CreateWorkflowRun(ctx, db.CreateWorkflowRunParams{
		WorkspaceID:      p.WorkspaceID,
		TemplateID:       p.TemplateID,
		TemplateSnapshot: snapJSON,
		SourceType:       p.SourceType,
		SourceID:         sourceID,
		IntakeIssueID:    intake.ID,
		Context:          runCtxJSON,
	})
	if err != nil {
		if isUniqueViolation(err) && sourceID.Valid {
			// Lost the idempotency race: the other pusher's run won the
			// UNIQUE(workspace, source_type, source_id, template) slot.
			// Rollback (undoing the intake issue) and return theirs.
			_ = tx.Rollback(ctx)
			existing, ferr := e.Queries.GetWorkflowRunBySource(ctx, db.GetWorkflowRunBySourceParams{
				WorkspaceID: p.WorkspaceID, SourceType: p.SourceType, SourceID: sourceID, TemplateID: p.TemplateID,
			})
			if ferr != nil {
				return db.WorkflowRun{}, false, fmt.Errorf("re-read run after idempotency race: %w", ferr)
			}
			return existing, false, nil
		}
		return db.WorkflowRun{}, false, fmt.Errorf("create run: %w", err)
	}
	firstStep, err := activateStepTx(ctx, qtx, run.ID, startNode.NodeKey)
	if err != nil {
		return db.WorkflowRun{}, false, fmt.Errorf("activate start node: %w", err)
	}
	if next := snap.NextAfterAll(startNode.NodeKey); len(next) > 0 {
		for _, n := range next {
			if err := preCreateStepTx(ctx, qtx, run.ID, n.NodeKey); err != nil {
				return db.WorkflowRun{}, false, fmt.Errorf("pre-create next node: %w", err)
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return db.WorkflowRun{}, false, fmt.Errorf("commit: %w", err)
	}

	e.publishIssueCreated(intake)
	// WS fanout (post-commit): the run exists as running and the start
	// node's step is active. activateNode below emits for anything it
	// transitions further (acceptance park, end completion).
	e.publishRunUpdated(run, RunRunning)
	e.publishStepUpdated(run, firstStep.ID, StepActive)
	if err := e.activateNode(ctx, run, snap, startNode, firstStep, nil); err != nil {
		return run, true, err
	}
	return run, true, nil
}

// ---------------------------------------------------------------------------
// SignalVerdict — verdict consumption and advancement (design.md §4.4)
// ---------------------------------------------------------------------------

// signalAction describes the post-commit side effects of one consumed
// verdict. Phase 1 (state machine) runs inside the transaction; phase 2
// (issue flips, dispatch, notifications) runs after commit.
type signalAction struct {
	kind     string // advance | retry | escalate | blocked | complete | none
	run      db.WorkflowRun
	snap     *Snapshot
	prevStep db.StepInstance // the step that just terminated
	nextNode *SnapshotNode   // advance/retry: node to activate
	nextStep db.StepInstance // advance/retry: its fresh active step
}

// SignalVerdict consumes the verdict currently attached to a step and
// advances the state machine. Idempotent (blueprint §8.1 推进幂等): the step
// is re-read under the run row lock inside the transaction and an
// already-terminal step makes the whole signal a no-op.
func (e *Engine) SignalVerdict(ctx context.Context, stepInstanceID pgtype.UUID) error {
	action, err := e.consumeVerdictTx(ctx, stepInstanceID)
	if err != nil {
		return err
	}
	return e.runSignalAction(ctx, action)
}

func (e *Engine) consumeVerdictTx(ctx context.Context, stepInstanceID pgtype.UUID) (signalAction, error) {
	none := signalAction{kind: "none"}
	tx, err := e.TxStarter.Begin(ctx)
	if err != nil {
		return none, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := e.Queries.WithTx(tx)

	step, err := qtx.GetStepInstanceForUpdate(ctx, stepInstanceID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return none, ErrStepNotFound
		}
		return none, fmt.Errorf("lock step: %w", err)
	}
	if isTerminalStepStatus(step.Status) {
		return none, nil
	}
	// Serialize concurrent signals on the run row (blueprint §8.1).
	run, err := qtx.GetWorkflowRunForUpdate(ctx, step.RunID)
	if err != nil {
		return none, fmt.Errorf("lock run: %w", err)
	}
	if run.Status != RunRunning && run.Status != RunWaitingAcceptance {
		return none, nil
	}
	snap, err := ParseSnapshot(run.TemplateSnapshot)
	if err != nil {
		return none, err
	}
	node := snap.NodeByKey(step.NodeKey)
	if node == nil {
		return none, fmt.Errorf("workflow: node %q missing from run snapshot", step.NodeKey)
	}
	verdict, err := qtx.GetVerdictByStepInstance(ctx, step.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return none, ErrVerdictNotFound
		}
		return none, fmt.Errorf("read verdict: %w", err)
	}

	action := signalAction{kind: "none", run: run, snap: snap, prevStep: step}
	switch verdict.Result {
	case VerdictPass:
		submission, err := qtx.GetSubmissionByStepInstance(ctx, step.ID)
		if err != nil {
			return none, fmt.Errorf("read submission: %w", err)
		}
		if len(submission.ExitFields) > 0 {
			if _, err := qtx.UpdateStepInstanceExitFields(ctx, db.UpdateStepInstanceExitFieldsParams{
				ID: step.ID, ExitFields: submission.ExitFields,
			}); err != nil {
				return none, fmt.Errorf("copy exit fields: %w", err)
			}
		}
		if !e.transitionStepTx(ctx, qtx, step, StepPassed, "verdict", verdictPayload(verdict)) {
			return none, nil // lost the guard race; another consumer advanced
		}
		if next := snap.NextAfterAll(step.NodeKey); len(next) > 0 {
			// Wave 0 P0 invariant: agent/acceptance nodes have out-degree
			// 1, so this loop runs once. fan_out would branch (N>1) but
			// fan_out activation lands in Wave 2 — for now the loop body's
			// action.nextNode/nextStep assignment picks the last iter, which
			// is fine because the slice has exactly one element here.
			for _, nxt := range next {
				nextStep, err := activateStepTx(ctx, qtx, run.ID, nxt.NodeKey)
				if err != nil {
					return none, err
				}
				for _, after := range lookaheadTargets(snap, &nxt) {
					if err := preCreateStepTx(ctx, qtx, run.ID, after.NodeKey); err != nil {
						return none, err
					}
				}
				n := nxt // capture for address (Go loop var safety)
				action.kind, action.nextNode, action.nextStep = "advance", &n, nextStep
			}
		} else {
			// Chain tail passed: no end node modeled — complete the run.
			action.kind = "complete"
		}

	case VerdictFail:
		if step.Attempt < node.Config.EffectiveMaxAttempts() {
			if !e.transitionStepTx(ctx, qtx, step, StepFailed, "verdict", verdictPayload(verdict)) {
				return none, nil
			}
			fresh, err := newAttemptStepTx(ctx, qtx, run.ID, step.NodeKey, step.Attempt+1)
			if err != nil {
				return none, err
			}
			action.kind, action.nextNode, action.nextStep = "retry", node, fresh
		} else {
			if !e.transitionStepTx(ctx, qtx, step, StepFailed, "verdict", verdictPayload(verdict)) {
				return none, nil
			}
			if !e.pauseRunTx(ctx, qtx, run) {
				return none, nil
			}
			action.kind, action.nextNode = "escalate", node
		}

	case VerdictBlocked:
		if !e.transitionStepTx(ctx, qtx, step, StepBlocked, "verdict", verdictPayload(verdict)) {
			return none, nil
		}
		if !e.pauseRunTx(ctx, qtx, run) {
			return none, nil
		}
		action.kind = "blocked"

	default:
		return none, fmt.Errorf("workflow: unknown verdict result %q", verdict.Result)
	}

	if err := tx.Commit(ctx); err != nil {
		return none, fmt.Errorf("commit: %w", err)
	}
	return action, nil
}

// runSignalAction executes the post-commit side effects of a consumed
// verdict. Errors are logged, not returned: the state machine already
// committed, and the P1 sweeper reconciles any side effect that failed.
func (e *Engine) runSignalAction(ctx context.Context, a signalAction) error {
	if a.kind != "none" {
		e.emitSignalEvents(a)
	}
	switch a.kind {
	case "none":
		return nil
	case "advance":
		e.closeStepIssue(ctx, a.prevStep, "done")
		return e.activateNode(ctx, a.run, a.snap, a.nextNode, a.nextStep, nil)
	case "complete":
		e.closeStepIssue(ctx, a.prevStep, "done")
		return e.completeRun(ctx, a.run)
	case "retry":
		e.closeStepIssue(ctx, a.prevStep, "cancelled")
		return e.activateNode(ctx, a.run, a.snap, a.nextNode, a.nextStep, nil)
	case "escalate":
		e.closeStepIssue(ctx, a.prevStep, "cancelled")
		e.handoffToHuman(ctx, a.run,
			fmt.Sprintf("Workflow paused: node %q failed attempt %d of %d", a.prevStep.NodeKey, a.prevStep.Attempt, a.nextNode.Config.EffectiveMaxAttempts()),
			"workflow_escalated")
		return nil
	case "blocked":
		e.notifyInitiator(ctx, a.run, "workflow_blocked", "action_required",
			fmt.Sprintf("Workflow paused: node %q is blocked", a.prevStep.NodeKey),
			map[string]any{"run_id": util.UUIDToString(a.run.ID), "node_key": a.prevStep.NodeKey})
		return nil
	}
	return nil
}

// emitSignalEvents publishes the WS events for the transitions
// consumeVerdictTx already committed (events.go emission rule). The new
// statuses derive from action.kind — prevStep carries its PRE-transition
// status, so it cannot be re-read for the payload.
func (e *Engine) emitSignalEvents(a signalAction) {
	stepStatus := map[string]string{
		"advance":  StepPassed,
		"complete": StepPassed,
		"retry":    StepFailed,
		"escalate": StepFailed,
		"blocked":  StepBlocked,
	}[a.kind]
	e.publishStepUpdated(a.run, a.prevStep.ID, stepStatus)
	switch a.kind {
	case "advance", "retry":
		e.publishStepUpdated(a.run, a.nextStep.ID, StepActive)
	case "escalate", "blocked":
		e.publishRunUpdated(a.run, RunPaused)
	}
}

// ---------------------------------------------------------------------------
// Submission + verdict recording (service layer of the dual validation)
// ---------------------------------------------------------------------------

// SubmissionInput is one agent submission for a step (R4). ExitFields are
// validated against the node's frozen schema at BOTH layers: the handler
// (transport) and here (design.md §1 D-9). Artifacts are likewise checked at
// both layers for the D-11 durable-reference rule (no local/workdir paths).
type SubmissionInput struct {
	Status         string
	Gaps           json.RawMessage
	Artifacts      json.RawMessage
	ExitFields     map[string]any
	RawSummary     string
	IdempotencyKey string
}

// RecordSubmission writes a submission for the step bound to a task and, for
// executor-role steps, derives the system verdict in the same transaction
// (verdict actor model, design.md §4.3) and consumes it. Idempotent via
// (step, idempotency_key): a replay returns the original row with
// created=false and re-signals (SignalVerdict no-ops on terminal steps), so
// a retry after a mid-flight crash still drives advancement.
func (e *Engine) RecordSubmission(ctx context.Context, taskID pgtype.UUID, in SubmissionInput) (db.Submission, bool, error) {
	step, node, err := e.LoadStepNode(ctx, taskID)
	if err != nil {
		return db.Submission{}, false, err
	}
	switch in.Status {
	case SubmissionDone, SubmissionDoneWithConcerns, SubmissionBlocked, SubmissionNeedsContext:
	default:
		return db.Submission{}, false, fmt.Errorf("workflow: unknown submission status %q", in.Status)
	}
	if fieldErrs := ValidateExitFieldsForStatus(in.Status, node.Config.ExitFields, in.ExitFields); len(fieldErrs) > 0 {
		return db.Submission{}, false, &ExitFieldsValidationError{Fields: fieldErrs}
	}
	// D-11 (layer 2): artifacts carry durable references only — local
	// filesystem/workdir paths are garbage collected and void the trail.
	if fieldErrs := ValidateArtifacts(in.Artifacts); len(fieldErrs) > 0 {
		return db.Submission{}, false, &ArtifactsValidationError{Fields: fieldErrs}
	}

	if in.IdempotencyKey != "" {
		existing, err := e.Queries.GetSubmissionByIdempotencyKey(ctx, db.GetSubmissionByIdempotencyKeyParams{
			StepInstanceID: step.ID,
			IdempotencyKey: pgtype.Text{String: in.IdempotencyKey, Valid: true},
		})
		if err == nil {
			e.resignal(ctx, step)
			return existing, false, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return db.Submission{}, false, fmt.Errorf("idempotency check: %w", err)
		}
	}

	// Terminal-step guard (Gate W1 follow-up): a dead step (skipped old
	// attempt, failed-after-max) must not accrete a submission whose verdict
	// the state machine would ignore anyway. Placed after the idempotency
	// replay so a retried same-key write on a since-passed step still
	// returns its original row.
	if isTerminalStepStatus(step.Status) {
		return db.Submission{}, false, ErrStepTerminal
	}

	exitFieldsJSON, err := marshalExitFields(in.ExitFields)
	if err != nil {
		return db.Submission{}, false, fmt.Errorf("marshal exit fields: %w", err)
	}

	tx, err := e.TxStarter.Begin(ctx)
	if err != nil {
		return db.Submission{}, false, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := e.Queries.WithTx(tx)

	if existing, err := qtx.GetSubmissionByStepInstance(ctx, step.ID); err == nil {
		// Same-key retry that raced past the pre-check above (the winner
		// committed between the two reads): replay, don't conflict
		// (blueprint §8.2 — same semantics as the post-collision fallback).
		if in.IdempotencyKey != "" && existing.IdempotencyKey.Valid && existing.IdempotencyKey.String == in.IdempotencyKey {
			_ = tx.Rollback(ctx)
			e.resignal(ctx, step)
			return existing, false, nil
		}
		return db.Submission{}, false, ErrSubmissionExists
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return db.Submission{}, false, fmt.Errorf("check existing submission: %w", err)
	}

	sub, err := qtx.CreateSubmission(ctx, db.CreateSubmissionParams{
		StepInstanceID: step.ID,
		TaskID:         taskID,
		Status:         in.Status,
		Gaps:           in.Gaps,
		Artifacts:      in.Artifacts,
		ExitFields:     exitFieldsJSON,
		RawSummary:     pgtype.Text{String: in.RawSummary, Valid: in.RawSummary != ""},
		IdempotencyKey: pgtype.Text{String: in.IdempotencyKey, Valid: in.IdempotencyKey != ""},
	})
	if err != nil {
		if isUniqueViolation(err) {
			_ = tx.Rollback(ctx)
			// Another writer won either UNIQUE(step_instance_id) or the
			// (step, key) index. A same-key retry is a replay; anything
			// else is a genuine conflict.
			if in.IdempotencyKey != "" {
				if existing, ferr := e.Queries.GetSubmissionByIdempotencyKey(ctx, db.GetSubmissionByIdempotencyKeyParams{
					StepInstanceID: step.ID,
					IdempotencyKey: pgtype.Text{String: in.IdempotencyKey, Valid: true},
				}); ferr == nil {
					e.resignal(ctx, step)
					return existing, false, nil
				}
			}
			return db.Submission{}, false, ErrSubmissionExists
		}
		return db.Submission{}, false, fmt.Errorf("create submission: %w", err)
	}

	// Verdict actor model: executor steps get a system-derived verdict in
	// the same transaction; evaluator steps wait for an explicit verdict.
	if node.Config.EffectiveRole() == RoleExecutor {
		result, rootCause, evidence := deriveSystemVerdict(in)
		if _, err := qtx.CreateVerdict(ctx, db.CreateVerdictParams{
			SubmissionID:   sub.ID,
			StepInstanceID: step.ID,
			Result:         result,
			VerdictBy:      "system",
			RootCause:      pgtype.Text{String: rootCause, Valid: rootCause != ""},
			Evidence:       evidence,
		}); err != nil {
			return db.Submission{}, false, fmt.Errorf("derive system verdict: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return db.Submission{}, false, fmt.Errorf("commit: %w", err)
	}
	e.resignal(ctx, step)
	return sub, true, nil
}

// resignal consumes any verdict now attached to the step; it is a no-op
// when none exists (evaluator submission) or the step is already terminal.
func (e *Engine) resignal(ctx context.Context, step db.StepInstance) {
	if err := e.SignalVerdict(ctx, step.ID); err != nil &&
		!errors.Is(err, ErrVerdictNotFound) && !errors.Is(err, ErrStepNotFound) {
		slog.Warn("workflow: signal verdict failed",
			"step_instance_id", util.UUIDToString(step.ID), "error", err)
	}
}

// deriveSystemVerdict maps the agent-declared submission status to the
// system verdict (design.md §4.3): DONE → pass; DONE_WITH_CONCERNS → pass
// with the concerns recorded as evidence; BLOCKED / NEEDS_CONTEXT → blocked.
func deriveSystemVerdict(in SubmissionInput) (result, rootCause string, evidence []byte) {
	switch in.Status {
	case SubmissionDone:
		return VerdictPass, "", nil
	case SubmissionDoneWithConcerns:
		ev, _ := json.Marshal(map[string]any{"concerns": json.RawMessage(orEmptyArray(in.Gaps))})
		return VerdictPass, "", ev
	case SubmissionBlocked:
		return VerdictBlocked, orSummary(in.RawSummary, "agent reported BLOCKED"), nil
	case SubmissionNeedsContext:
		return VerdictBlocked, orSummary(in.RawSummary, "agent reported NEEDS_CONTEXT"), nil
	}
	return VerdictBlocked, "unknown submission status", nil
}

func orEmptyArray(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("[]")
	}
	return raw
}

func orSummary(s, fallback string) string {
	if s != "" {
		return s
	}
	return fallback
}

// marshalExitFields normalizes a nil map to "{}" so stored exit_fields is
// always a JSON object — never JSON null (downstream readers: the pass-time
// step copy, step-context assembly, frontend zod parsing).
func marshalExitFields(fields map[string]any) ([]byte, error) {
	if fields == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(fields)
}

// VerdictInput is one evaluator verdict write (R3 verdict actor model).
// ExitFields feeds the auto-created minimal submission when the step has
// none yet — it must satisfy the node's required schema (review #10).
type VerdictInput struct {
	Result     string
	RootCause  string
	Confidence *float64
	Evidence   json.RawMessage
	ExitFields map[string]any
}

// RecordVerdict writes an evaluator verdict for the step bound to a task.
// When the step has no submission yet, a minimal one is created in the same
// transaction — passing the SAME exit-fields validation (a node with
// required exit fields rejects a verdict that omits them). The verdict then
// drives SignalVerdict.
func (e *Engine) RecordVerdict(ctx context.Context, taskID pgtype.UUID, in VerdictInput) (db.Verdict, error) {
	step, node, err := e.LoadStepNode(ctx, taskID)
	if err != nil {
		return db.Verdict{}, err
	}
	if node.Config.EffectiveRole() != RoleEvaluator {
		return db.Verdict{}, ErrNotEvaluatorStep
	}
	switch in.Result {
	case VerdictPass, VerdictFail, VerdictBlocked:
	default:
		return db.Verdict{}, fmt.Errorf("workflow: unknown verdict result %q", in.Result)
	}
	// Terminal-step guard (Gate W1 follow-up): without it a verdict aimed at
	// a skipped/failed step auto-creates a noise submission+verdict pair
	// whose signal the state machine then ignores.
	if isTerminalStepStatus(step.Status) {
		return db.Verdict{}, ErrStepTerminal
	}

	tx, err := e.TxStarter.Begin(ctx)
	if err != nil {
		return db.Verdict{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := e.Queries.WithTx(tx)

	sub, err := qtx.GetSubmissionByStepInstance(ctx, step.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		// Auto-create the minimal submission the verdict hangs from
		// (UNIQUE(submission_id) + FK hard contract, design.md §7 #6) —
		// through the same 准出 validation as a direct submission.
		if fieldErrs := ValidateExitFields(node.Config.ExitFields, in.ExitFields); len(fieldErrs) > 0 {
			return db.Verdict{}, &ExitFieldsValidationError{Fields: fieldErrs}
		}
		exitFieldsJSON, merr := marshalExitFields(in.ExitFields)
		if merr != nil {
			return db.Verdict{}, fmt.Errorf("marshal exit fields: %w", merr)
		}
		sub, err = qtx.CreateSubmission(ctx, db.CreateSubmissionParams{
			StepInstanceID: step.ID,
			TaskID:         taskID,
			Status:         SubmissionDone,
			ExitFields:     exitFieldsJSON,
		})
		if err != nil {
			return db.Verdict{}, fmt.Errorf("auto-create submission: %w", err)
		}
	} else if err != nil {
		return db.Verdict{}, fmt.Errorf("read submission: %w", err)
	}

	verdict, err := qtx.CreateVerdict(ctx, db.CreateVerdictParams{
		SubmissionID:   sub.ID,
		StepInstanceID: step.ID,
		Result:         in.Result,
		VerdictBy:      "agent",
		RootCause:      pgtype.Text{String: in.RootCause, Valid: in.RootCause != ""},
		Confidence:     numericFromFloat(in.Confidence),
		Evidence:       in.Evidence,
	})
	if err != nil {
		if isUniqueViolation(err) {
			return db.Verdict{}, ErrVerdictExists
		}
		return db.Verdict{}, fmt.Errorf("create verdict: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return db.Verdict{}, fmt.Errorf("commit: %w", err)
	}
	e.resignal(ctx, step)
	return verdict, nil
}

// ---------------------------------------------------------------------------
// Acceptance decisions (design.md §4.5)
// ---------------------------------------------------------------------------

// ApproveAcceptance decides a pending acceptance as approved: the acceptance
// node's step passes and the chain advances (final acceptance rolls into the
// end node / run completion). Guarded on status='pending' — a double-click
// loser gets ErrAcceptanceConflict (blueprint §8.3). decider is the deciding
// member (member.id); an invalid UUID marks the system auto_pass path, which
// leaves the activation-time reviewer untouched and writes trigger "system".
func (e *Engine) ApproveAcceptance(ctx context.Context, runID, acceptanceID, decider pgtype.UUID) error {
	action, err := e.decideAcceptanceTx(ctx, runID, acceptanceID, decider, "approved", "", "", nil)
	if err != nil {
		return err
	}
	switch action.kind {
	case "advance":
		e.publishStepUpdated(action.run, action.prevStep.ID, StepPassed)
		e.publishStepUpdated(action.run, action.nextStep.ID, StepActive)
		e.publishRunUpdated(action.run, RunRunning)
		return e.activateNode(ctx, action.run, action.snap, action.nextNode, action.nextStep, nil)
	case "complete":
		e.publishStepUpdated(action.run, action.prevStep.ID, StepPassed)
		e.publishRunUpdated(action.run, RunRunning)
		return e.completeRun(ctx, action.run)
	}
	return nil
}

// RejectAcceptance decides a pending acceptance as rejected and starts
// targeted rework (design.md §4.4): run returns to running, then either the
// rejection circuit breaker trips (≥3 rejections in the run → paused +
// human handoff) or RequestRework re-enters the target node. decider is the
// rejecting member (member.id).
func (e *Engine) RejectAcceptance(ctx context.Context, runID, acceptanceID, decider pgtype.UUID, targetNodeKey, reason string) error {
	if targetNodeKey == "" {
		return errors.New("workflow: reject_to_node_key is required")
	}
	if reason == "" {
		return errors.New("workflow: reject reason is required")
	}

	// Assemble rework context BEFORE deciding so it is stored on the
	// acceptance row in the same guarded write.
	run, err := e.Queries.GetWorkflowRun(ctx, runID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrRunNotFound
		}
		return fmt.Errorf("get run: %w", err)
	}
	rc, err := e.assembleReworkContext(ctx, runID, targetNodeKey, reason, "acceptance_reject")
	if err != nil {
		return err
	}
	rcJSON, err := json.Marshal(rc)
	if err != nil {
		return fmt.Errorf("marshal rework context: %w", err)
	}

	action, err := e.decideAcceptanceTx(ctx, runID, acceptanceID, decider, "rejected", targetNodeKey, reason, rcJSON)
	if err != nil {
		return err
	}
	run = action.run
	// The run left waiting_acceptance; the breaker pause or the rework
	// re-activation below announces whatever it lands on next.
	e.publishRunUpdated(run, RunRunning)

	// Circuit breaker ② (design.md §4.4): total rejections in this run.
	rejections, err := e.Queries.CountRejectionsForRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("count rejections: %w", err)
	}
	if rejections >= circuitBreakerLimit {
		if err := e.pauseAndHandoff(ctx, run,
			fmt.Sprintf("Workflow paused: acceptance rejected %d times", rejections),
			"workflow_circuit_breaker"); err != nil {
			return err
		}
		return nil
	}
	return e.RequestRework(ctx, runID, targetNodeKey, rc)
}

// decideAcceptanceTx runs the shared guarded write behind approve/reject:
// decide the acceptance (pending-guarded), pass the acceptance node's step,
// flip the run back to running, and — on approve — advance the chain.
// decider.Valid marks a human decision: the acceptance row's reviewer_id is
// restamped to the decider and the step transition records trigger "human";
// the system auto_pass path passes an invalid UUID and records "system".
func (e *Engine) decideAcceptanceTx(ctx context.Context, runID, acceptanceID, decider pgtype.UUID, decision, targetNodeKey, reason string, reworkCtx []byte) (signalAction, error) {
	none := signalAction{kind: "none"}
	tx, err := e.TxStarter.Begin(ctx)
	if err != nil {
		return none, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := e.Queries.WithTx(tx)

	acc, err := qtx.DecideAcceptance(ctx, db.DecideAcceptanceParams{
		NewStatus:       decision,
		ReviewerID:      decider,
		RejectReason:    pgtype.Text{String: reason, Valid: reason != ""},
		RejectToNodeKey: pgtype.Text{String: targetNodeKey, Valid: targetNodeKey != ""},
		ReworkContext:   reworkCtx,
		ID:              acceptanceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return none, ErrAcceptanceConflict
		}
		return none, fmt.Errorf("decide acceptance: %w", err)
	}
	if acc.RunID != runID {
		return none, ErrAcceptanceNotFound
	}
	// Serialize with in-flight verdict consumption (blueprint §8.1).
	run, err := qtx.GetWorkflowRunForUpdate(ctx, runID)
	if err != nil {
		return none, fmt.Errorf("lock run: %w", err)
	}
	if run.Status != RunWaitingAcceptance {
		return none, ErrRunNotActive
	}
	snap, err := ParseSnapshot(run.TemplateSnapshot)
	if err != nil {
		return none, err
	}
	if decision == "rejected" {
		// Validate the rework target BEFORE deciding: once the acceptance
		// row flips to rejected it cannot be re-decided, so a target that
		// RequestRework would refuse (unknown node / in-flight step) must
		// fail here, leaving the acceptance still pending.
		if snap.NodeByKey(targetNodeKey) == nil {
			return none, ErrReworkTargetUnknown
		}
		latest, lerr := qtx.GetLatestStepInstanceForNode(ctx, db.GetLatestStepInstanceForNodeParams{
			RunID: runID, NodeKey: targetNodeKey,
		})
		if lerr != nil {
			if errors.Is(lerr, pgx.ErrNoRows) {
				return none, ErrReworkTargetUnknown
			}
			return none, fmt.Errorf("read rework target step: %w", lerr)
		}
		if !isTerminalStepStatus(latest.Status) {
			return none, ErrReworkTargetActive
		}
	}
	step, err := qtx.GetStepInstanceForUpdate(ctx, acc.StepInstanceID)
	if err != nil {
		return none, fmt.Errorf("lock acceptance step: %w", err)
	}

	action := signalAction{kind: "none", run: run, snap: snap, prevStep: step}
	if decision == "approved" {
		triggerBy := "system"
		if decider.Valid {
			triggerBy = "human"
		}
		if !e.transitionStepTx(ctx, qtx, step, StepPassed, triggerBy, map[string]any{"acceptance_id": util.UUIDToString(acc.ID)}) {
			return none, ErrAcceptanceConflict
		}
		if next := snap.NextAfterAll(step.NodeKey); len(next) > 0 {
			for _, nxt := range next {
				nextStep, err := activateStepTx(ctx, qtx, run.ID, nxt.NodeKey)
				if err != nil {
					return none, err
				}
				for _, after := range lookaheadTargets(snap, &nxt) {
					if err := preCreateStepTx(ctx, qtx, run.ID, after.NodeKey); err != nil {
						return none, err
					}
				}
				n := nxt
				action.kind, action.nextNode, action.nextStep = "advance", &n, nextStep
			}
		} else {
			action.kind = "complete"
		}
	}
	// Both decisions return the run to running; approve may immediately
	// re-park it (next acceptance) or complete it in the post-commit flow.
	if _, err := qtx.UpdateWorkflowRunStatus(ctx, db.UpdateWorkflowRunStatusParams{
		NewStatus: RunRunning, ID: run.ID, ExpectedStatus: RunWaitingAcceptance,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return none, ErrAcceptanceConflict
		}
		return none, fmt.Errorf("resume run: %w", err)
	}
	run.Status = RunRunning
	action.run = run

	if err := tx.Commit(ctx); err != nil {
		return none, fmt.Errorf("commit: %w", err)
	}
	return action, nil
}

// ---------------------------------------------------------------------------
// Shared run/step helpers (tx-scoped)
// ---------------------------------------------------------------------------

// activateStepTx promotes a pre-created pending step to active, or inserts a
// fresh active attempt when none exists (retry/rework/re-run after
// invalidation). The uq_step_instance_attempt UNIQUE NULLS NOT DISTINCT
// constraint makes a duplicate activation a unique violation instead of a
// double dispatch.
func activateStepTx(ctx context.Context, qtx *db.Queries, runID pgtype.UUID, nodeKey string) (db.StepInstance, error) {
	pending, err := qtx.GetStepInstanceForNodeWithStatus(ctx, db.GetStepInstanceForNodeWithStatusParams{
		RunID: runID, NodeKey: nodeKey, Status: StepPending,
	})
	switch {
	case err == nil:
		updated, err := qtx.UpdateStepInstanceStatus(ctx, db.UpdateStepInstanceStatusParams{
			NewStatus:      StepActive,
			StartedAt:      pgtype.Timestamptz{Time: time.Now(), Valid: true},
			ID:             pending.ID,
			ExpectedStatus: StepPending,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return db.StepInstance{}, fmt.Errorf("workflow: pending step %s vanished during activation", nodeKey)
			}
			return db.StepInstance{}, fmt.Errorf("promote pending step: %w", err)
		}
		writeTransitionTx(ctx, qtx, runID, pending.ID, StepPending, StepActive, pending.Attempt, "engine", nil)
		return updated, nil
	case errors.Is(err, pgx.ErrNoRows):
		latest, err := qtx.GetLatestStepInstanceForNode(ctx, db.GetLatestStepInstanceForNodeParams{
			RunID: runID, NodeKey: nodeKey,
		})
		attempt := int32(1)
		switch {
		case err == nil:
			attempt = latest.Attempt + 1
		case errors.Is(err, pgx.ErrNoRows):
		default:
			return db.StepInstance{}, fmt.Errorf("read latest step for %q: %w", nodeKey, err)
		}
		return newAttemptStepTx(ctx, qtx, runID, nodeKey, attempt)
	default:
		return db.StepInstance{}, fmt.Errorf("check pending step for %q: %w", nodeKey, err)
	}
}

// newAttemptStepTx inserts a fresh active step row for a node's next attempt.
func newAttemptStepTx(ctx context.Context, qtx *db.Queries, runID pgtype.UUID, nodeKey string, attempt int32) (db.StepInstance, error) {
	row, err := qtx.CreateStepInstance(ctx, db.CreateStepInstanceParams{
		RunID: runID, NodeKey: nodeKey, Status: StepActive, Attempt: attempt,
	})
	if err != nil {
		return db.StepInstance{}, fmt.Errorf("create step for %q attempt %d: %w", nodeKey, attempt, err)
	}
	writeTransitionTx(ctx, qtx, runID, row.ID, "none", StepActive, attempt, "engine", nil)
	return row, nil
}

// preCreateStepTx materializes the next node's pending row (one node ahead
// only, inventory D-3). Idempotent: an existing pending row wins.
func preCreateStepTx(ctx context.Context, qtx *db.Queries, runID pgtype.UUID, nodeKey string) error {
	_, err := qtx.GetStepInstanceForNodeWithStatus(ctx, db.GetStepInstanceForNodeWithStatusParams{
		RunID: runID, NodeKey: nodeKey, Status: StepPending,
	})
	switch {
	case err == nil:
		return nil
	case errors.Is(err, pgx.ErrNoRows):
		latest, lerr := qtx.GetLatestStepInstanceForNode(ctx, db.GetLatestStepInstanceForNodeParams{
			RunID: runID, NodeKey: nodeKey,
		})
		attempt := int32(1)
		switch {
		case lerr == nil:
			attempt = latest.Attempt + 1
		case errors.Is(lerr, pgx.ErrNoRows):
		default:
			return fmt.Errorf("read latest step for %q: %w", nodeKey, lerr)
		}
		row, err := qtx.CreateStepInstance(ctx, db.CreateStepInstanceParams{
			RunID: runID, NodeKey: nodeKey, Status: StepPending, Attempt: attempt,
		})
		if err != nil {
			if isUniqueViolation(err) {
				return nil // a concurrent pre-create landed first
			}
			return fmt.Errorf("pre-create step for %q: %w", nodeKey, err)
		}
		writeTransitionTx(ctx, qtx, runID, row.ID, "none", StepPending, attempt, "engine", nil)
		return nil
	default:
		return fmt.Errorf("check pending step for %q: %w", nodeKey, err)
	}
}

// lookaheadTargets returns the nodes whose pending rows should be
// pre-created (one level ahead) when `node` has just been activated.
// Pre-creation mirrors P0's inventory D-3 "one node ahead" semantics but
// adapts to DAG node types:
//
//   - fan_out: no lookahead. fan_out's downstreams are sibling child steps
//     that fan_out activation expands dynamically (Wave 2); pre-creating
//     them here would race with the fan_out trigger.
//   - converge: pre-create converge's downstream (usually 1, the
//     post-converge linear node).
//   - agent / acceptance / end: pre-create the single downstream (P0
//     behavior; out-degree 1 → slice of 1).
//
// For P0 linear templates the result is identical to the original
// `snap.NextAfter(next.NodeKey)` single-element form.
func lookaheadTargets(snap *Snapshot, node *SnapshotNode) []SnapshotNode {
	if node == nil {
		return nil
	}
	switch node.Type {
	case NodeTypeFanOut:
		return nil
	default:
		return snap.NextAfterAll(node.NodeKey)
	}
}

// transitionStepTx performs one guarded step status change plus its
// step_transition row (design.md §4.6). Returns false when the guard lost a
// race (zero rows) — the caller abandons and re-reads.
func (e *Engine) transitionStepTx(ctx context.Context, qtx *db.Queries, step db.StepInstance, toStatus, triggerBy string, payload map[string]any) bool {
	now := time.Now()
	params := db.UpdateStepInstanceStatusParams{
		NewStatus:      toStatus,
		ID:             step.ID,
		ExpectedStatus: step.Status,
	}
	if isTerminalStepStatus(toStatus) {
		params.FinishedAt = pgtype.Timestamptz{Time: now, Valid: true}
	}
	if _, err := qtx.UpdateStepInstanceStatus(ctx, params); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false
		}
		// Surface real errors via panic-free logging + false: the tx rolls
		// back either way, and the caller's re-read observes reality.
		slog.Error("workflow: guarded step transition failed",
			"step_instance_id", util.UUIDToString(step.ID), "to", toStatus, "error", err)
		return false
	}
	writeTransitionTx(ctx, qtx, step.RunID, step.ID, step.Status, toStatus, step.Attempt, triggerBy, payload)
	return true
}

// writeTransitionTx appends one step_transition row. The dedup unique index
// (step, from, to, attempt) + ON CONFLICT DO NOTHING make a retried write a
// silent no-op, so circuit-breaker counting never double-counts.
func writeTransitionTx(ctx context.Context, qtx *db.Queries, runID, stepID pgtype.UUID, from, to string, attempt int32, triggerBy string, payload map[string]any) {
	var payloadJSON []byte
	if payload != nil {
		var merr error
		payloadJSON, merr = json.Marshal(payload)
		if merr != nil {
			// The transition row is the audit-critical write; a payload that
			// cannot marshal degrades to NULL rather than dropping history.
			slog.Error("workflow: transition payload marshal failed", "error", merr)
			payloadJSON = nil
		}
	}
	if _, err := qtx.CreateStepTransition(ctx, db.CreateStepTransitionParams{
		RunID:          runID,
		StepInstanceID: stepID,
		FromStatus:     from,
		ToStatus:       to,
		Attempt:        attempt,
		TriggerBy:      triggerBy,
		Payload:        payloadJSON,
	}); err != nil {
		slog.Error("workflow: step transition write failed",
			"step_instance_id", util.UUIDToString(stepID), "from", from, "to", to, "error", err)
	}
}

// pauseRunTx flips run → paused under the guarded UPDATE (from running or
// waiting_acceptance). False means another transition beat us.
func (e *Engine) pauseRunTx(ctx context.Context, qtx *db.Queries, run db.WorkflowRun) bool {
	for _, expected := range []string{RunRunning, RunWaitingAcceptance} {
		if _, err := qtx.UpdateWorkflowRunStatus(ctx, db.UpdateWorkflowRunStatusParams{
			NewStatus: RunPaused, ID: run.ID, ExpectedStatus: expected,
		}); err == nil {
			return true
		} else if !errors.Is(err, pgx.ErrNoRows) {
			slog.Error("workflow: pause run failed", "run_id", util.UUIDToString(run.ID), "error", err)
			return false
		}
	}
	return false
}

func verdictPayload(v db.Verdict) map[string]any {
	return map[string]any{"verdict_id": util.UUIDToString(v.ID), "result": v.Result}
}

// ---------------------------------------------------------------------------
// Post-commit side effects
// ---------------------------------------------------------------------------

// closeStepIssue flips a terminated step's child issue to done/cancelled via
// the service layer (design.md §4.1 子 issue 生命周期). Best-effort: the
// state machine already committed.
func (e *Engine) closeStepIssue(ctx context.Context, step db.StepInstance, status string) {
	if !step.IssueID.Valid || e.Issues == nil {
		return
	}
	issue, err := e.Queries.GetIssue(ctx, step.IssueID)
	if err != nil {
		slog.Warn("workflow: load step issue failed", "issue_id", util.UUIDToString(step.IssueID), "error", err)
		return
	}
	if _, err := e.Issues.SetStatus(ctx, issue, status); err != nil {
		slog.Warn("workflow: close step issue failed",
			"issue_id", util.UUIDToString(step.IssueID), "status", status, "error", err)
	}
}

// completeRun finishes a run whose chain tail passed: run → completed,
// intake issue → done, completion inbox to the initiator (design.md §4.5).
func (e *Engine) completeRun(ctx context.Context, run db.WorkflowRun) error {
	now := time.Now()
	completed := false
	for _, expected := range []string{RunRunning, RunWaitingAcceptance} {
		if _, err := e.Queries.UpdateWorkflowRunStatus(ctx, db.UpdateWorkflowRunStatusParams{
			NewStatus:      RunCompleted,
			CompletedAt:    pgtype.Timestamptz{Time: now, Valid: true},
			ID:             run.ID,
			ExpectedStatus: expected,
		}); err == nil {
			completed = true
			break
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("complete run: %w", err)
		}
	}
	if !completed {
		return nil // another transition beat us (pause/cancel) — respect it
	}
	e.publishRunUpdated(run, RunCompleted)
	if run.IntakeIssueID.Valid && e.Issues != nil {
		if intake, err := e.Queries.GetIssue(ctx, run.IntakeIssueID); err == nil {
			if _, err := e.Issues.SetStatus(ctx, intake, "done"); err != nil {
				slog.Warn("workflow: close intake issue failed", "error", err)
			}
		}
	}
	e.notifyInitiator(ctx, run, "workflow_completed", "info",
		"Workflow run completed: "+intakeTitle(ctx, e.Queries, run),
		map[string]any{"run_id": util.UUIDToString(run.ID)})
	return nil
}

// pauseAndHandoff is the circuit-breaker landing: run → paused, intake issue
// reassigned to the human initiator, action_required inbox (design.md §4.4).
func (e *Engine) pauseAndHandoff(ctx context.Context, run db.WorkflowRun, reason, inboxType string) error {
	tx, err := e.TxStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := e.Queries.WithTx(tx)
	locked, err := qtx.GetWorkflowRunForUpdate(ctx, run.ID)
	if err != nil {
		return fmt.Errorf("lock run: %w", err)
	}
	if locked.Status == RunCompleted || locked.Status == RunCancelled || locked.Status == RunFailed {
		return nil
	}
	paused := false
	if locked.Status != RunPaused {
		if !e.pauseRunTx(ctx, qtx, locked) {
			return nil
		}
		paused = true
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	if paused {
		e.publishRunUpdated(run, RunPaused)
	}
	e.handoffToHuman(ctx, run, reason, inboxType)
	return nil
}

// handoffToHuman reassigns the intake issue to the run's responsible human
// and writes the action_required inbox row (post-commit, best-effort).
func (e *Engine) handoffToHuman(ctx context.Context, run db.WorkflowRun, reason, inboxType string) {
	human := e.responsibleHuman(ctx, run)
	if run.IntakeIssueID.Valid && e.Issues != nil && human.Valid {
		if intake, err := e.Queries.GetIssue(ctx, run.IntakeIssueID); err == nil {
			if _, err := e.Issues.ReassignToMember(ctx, intake, human); err != nil {
				slog.Warn("workflow: handoff reassign failed", "error", err)
			}
		}
	}
	e.notifyInitiator(ctx, run, inboxType, "action_required", reason,
		map[string]any{"run_id": util.UUIDToString(run.ID)})
}

// responsibleHuman returns the user id accountable for the run: the
// initiator when one exists, else the hook-designated reviewer (member id →
// user id). Hook-originated runs carry no initiator — the reviewer is their
// 负责人, and AC1's completion notification must reach them.
func (e *Engine) responsibleHuman(ctx context.Context, run db.WorkflowRun) pgtype.UUID {
	rc := ParseRunContext(run.Context)
	if initiator := rc.Initiator(); initiator.Valid {
		return initiator
	}
	if reviewer := rc.Reviewer(); reviewer.Valid {
		if member, err := e.Queries.GetMember(ctx, reviewer); err == nil {
			return member.UserID
		}
	}
	return pgtype.UUID{}
}

// notifyInitiator writes an inbox row for the run's responsible human on the
// intake issue and broadcasts inbox:new (payload mirrors the quick-create
// inbox shape so existing WS consumers render it unchanged).
func (e *Engine) notifyInitiator(ctx context.Context, run db.WorkflowRun, typ, severity, title string, details map[string]any) {
	human := e.responsibleHuman(ctx, run)
	if !human.Valid {
		slog.Info("workflow: no responsible human to notify", "run_id", util.UUIDToString(run.ID), "type", typ)
		return
	}
	e.notifyMember(ctx, run, human, typ, severity, title, details)
}

// notifyMember is notifyInitiator with an explicit recipient (acceptance
// reviewer).
func (e *Engine) notifyMember(ctx context.Context, run db.WorkflowRun, recipientUserID pgtype.UUID, typ, severity, title string, details map[string]any) {
	if details == nil {
		details = map[string]any{}
	}
	details["run_id"] = util.UUIDToString(run.ID)
	detailsJSON, _ := json.Marshal(details)
	item, err := e.Queries.CreateInboxItem(ctx, db.CreateInboxItemParams{
		WorkspaceID:   run.WorkspaceID,
		RecipientType: "member",
		RecipientID:   recipientUserID,
		Type:          typ,
		Severity:      severity,
		IssueID:       run.IntakeIssueID,
		Title:         title,
		ActorType:     pgtype.Text{String: "system", Valid: true},
		Details:       detailsJSON,
	})
	if err != nil {
		slog.Error("workflow: inbox write failed", "run_id", util.UUIDToString(run.ID), "type", typ, "error", err)
		return
	}
	if e.Bus == nil {
		return
	}
	issueStatus := ""
	if run.IntakeIssueID.Valid {
		if intake, err := e.Queries.GetIssue(ctx, run.IntakeIssueID); err == nil {
			issueStatus = intake.Status
		}
	}
	e.Bus.Publish(events.Event{
		Type:        protocol.EventInboxNew,
		WorkspaceID: util.UUIDToString(run.WorkspaceID),
		ActorType:   "system",
		Payload: map[string]any{"item": map[string]any{
			"id":             util.UUIDToString(item.ID),
			"workspace_id":   util.UUIDToString(item.WorkspaceID),
			"recipient_type": item.RecipientType,
			"recipient_id":   util.UUIDToString(item.RecipientID),
			"type":           item.Type,
			"severity":       item.Severity,
			"issue_id":       util.UUIDToPtr(item.IssueID),
			"title":          item.Title,
			"body":           util.TextToPtr(item.Body),
			"read":           item.Read,
			"archived":       item.Archived,
			"created_at":     util.TimestampToString(item.CreatedAt),
			"actor_type":     util.TextToPtr(item.ActorType),
			"details":        json.RawMessage(item.Details),
			"issue_status":   issueStatus,
		}},
	})
}

func (e *Engine) publishIssueCreated(issue db.Issue) {
	if e.Bus == nil {
		return
	}
	e.Bus.Publish(events.Event{
		Type:        protocol.EventIssueCreated,
		WorkspaceID: util.UUIDToString(issue.WorkspaceID),
		ActorType:   "system",
		Payload:     map[string]any{"issue_id": util.UUIDToString(issue.ID)},
	})
}

// ---------------------------------------------------------------------------
// Read helpers shared with the handler layer
// ---------------------------------------------------------------------------

// LoadStepNode resolves task → step → run → frozen snapshot node. It backs
// the handler's layer-1 validation (exit fields, verdict role check).
func (e *Engine) LoadStepNode(ctx context.Context, taskID pgtype.UUID) (db.StepInstance, *SnapshotNode, error) {
	step, err := e.Queries.GetStepInstanceByTask(ctx, taskID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.StepInstance{}, nil, ErrStepNotFound
		}
		return db.StepInstance{}, nil, fmt.Errorf("get step by task: %w", err)
	}
	run, err := e.Queries.GetWorkflowRun(ctx, step.RunID)
	if err != nil {
		return db.StepInstance{}, nil, fmt.Errorf("get run: %w", err)
	}
	snap, err := ParseSnapshot(run.TemplateSnapshot)
	if err != nil {
		return db.StepInstance{}, nil, err
	}
	node := snap.NodeByKey(step.NodeKey)
	if node == nil {
		return db.StepInstance{}, nil, fmt.Errorf("workflow: node %q missing from run snapshot", step.NodeKey)
	}
	return step, node, nil
}

// StepContext is the CLI `multica step context` payload (R10): node
// instructions, the immediate upstream node's exit fields, and this node's
// exit-fields schema — the full-fidelity sibling of the handoff note.
type StepContext struct {
	NodeKey            string            `json:"node_key"`
	NodeType           string            `json:"node_type"`
	NodeName           string            `json:"node_name"`
	Role               string            `json:"role"`
	Attempt            int32             `json:"attempt"`
	StepStatus         string            `json:"step_status"`
	Instructions       string            `json:"instructions"`
	ExitFieldsSchema   *ExitFieldsSchema `json:"exit_fields_schema,omitempty"`
	UpstreamNodeKey    string            `json:"upstream_node_key,omitempty"`
	UpstreamExitFields map[string]any    `json:"upstream_exit_fields,omitempty"`
}

// GetStepContext assembles the node context for the step bound to a task.
func (e *Engine) GetStepContext(ctx context.Context, taskID pgtype.UUID) (*StepContext, error) {
	step, node, err := e.LoadStepNode(ctx, taskID)
	if err != nil {
		return nil, err
	}
	run, err := e.Queries.GetWorkflowRun(ctx, step.RunID)
	if err != nil {
		return nil, fmt.Errorf("get run: %w", err)
	}
	snap, err := ParseSnapshot(run.TemplateSnapshot)
	if err != nil {
		return nil, err
	}
	sc := &StepContext{
		NodeKey:          node.NodeKey,
		NodeType:         node.Type,
		NodeName:         node.Name,
		Role:             node.Config.EffectiveRole(),
		Attempt:          step.Attempt,
		StepStatus:       step.Status,
		Instructions:     node.Config.Instructions,
		ExitFieldsSchema: node.Config.ExitFields,
	}
	if up := upstreamNodeOf(snap, node.NodeKey); up != nil {
		sc.UpstreamNodeKey = up.NodeKey
		sc.UpstreamExitFields = e.passedExitFields(ctx, step.RunID, up.NodeKey)
	}
	return sc, nil
}

// upstreamNodeOf returns the direct predecessor in the chain.
func upstreamNodeOf(snap *Snapshot, nodeKey string) *SnapshotNode {
	for _, e := range snap.Edges {
		if e.ToNodeKey == nodeKey {
			return snap.NodeByKey(e.FromNodeKey)
		}
	}
	return nil
}

// passedExitFields reads the latest passed step's exit fields for a node
// (copied onto the step at pass time by the verdict consumer).
func (e *Engine) passedExitFields(ctx context.Context, runID pgtype.UUID, nodeKey string) map[string]any {
	step, err := e.Queries.GetStepInstanceForNodeWithStatus(ctx, db.GetStepInstanceForNodeWithStatusParams{
		RunID: runID, NodeKey: nodeKey, Status: StepPassed,
	})
	if err != nil || len(step.ExitFields) == 0 {
		return nil
	}
	var fields map[string]any
	if err := json.Unmarshal(step.ExitFields, &fields); err != nil {
		return nil
	}
	return fields
}

func intakeTitle(ctx context.Context, q *db.Queries, run db.WorkflowRun) string {
	if run.IntakeIssueID.Valid {
		if intake, err := q.GetIssue(ctx, run.IntakeIssueID); err == nil {
			return intake.Title
		}
	}
	return util.UUIDToString(run.ID)
}

// isUniqueViolation reports whether err is a Postgres unique-constraint
// violation (SQLSTATE 23505) — the backstop behind every idempotency path.
func isUniqueViolation(err error) bool {
	var pgErr interface{ SQLState() string }
	if errors.As(err, &pgErr) {
		return pgErr.SQLState() == "23505"
	}
	return false
}

// numericFromFloat converts an optional confidence to pgtype.Numeric.
// Numeric.Scan accepts only string input, so format first (a raw float64
// errors and would silently store NULL).
func numericFromFloat(f *float64) pgtype.Numeric {
	if f == nil {
		return pgtype.Numeric{}
	}
	var n pgtype.Numeric
	if err := n.Scan(strconv.FormatFloat(*f, 'g', -1, 64)); err != nil {
		return pgtype.Numeric{}
	}
	return n
}
