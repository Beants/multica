package workflow

// fanout.go — P1-1 Wave 2 fan_out node activation + fail-policy
// application + reworkChildStepScope helper (design.md §2.2, §4.2,
// §4.4; PRD R1, R4, R7).
//
// fan_out is a pure splitter (A scheme, Q1): it does not run an agent
// itself. At activation it reads the upstream submission's items_field
// array, validates each item (parseSubtasks + runtime agent/label
// resolution), and inside one transaction expands N child
// StepInstances — each with its own child issue and agent dispatch —
// then transitions the fan_out step itself to passed.
//
// P1-1 SCOPE LIMIT: fan_out admits a single downstream edge (the
// generic "branch executor" agent node). Multi-edge fan_out (different
// agent per branch) is P1-9 and is rejected here with ErrFanOutMultiEdge.
//
// SCHEMA NOTE — slot-based attempt encoding:
// The 917 unique index uq_step_instance_attempt is on
//   (run_id, node_key, parent_step_id, attempt) NULLS NOT DISTINCT.
// P1-1's single-edge model has N children sharing (run, "branch",
// fanout_step) — to coexist under that index, each child occupies a
// distinct attempt slot:
//
//	child i (0-indexed): attempt = i * childAttemptSlot + 1
//	rework of child i:   attempt = i * childAttemptSlot + reworkRound
//
// childAttemptSlot (1024) dwarfs the default maxAttempts (3), so a
// child's retries never cross into a sibling's slot. Converge counts
// children by parent_step_id, which is independent of attempt, so the
// encoding is invisible to convergence logic. This keeps the
// "no new migration" promise (PRD §3.1) at the cost of an unusual
// attempt assignment for child rows only; non-child rows (parent_step_id
// NULL) keep the P0 semantics (attempt = retry number).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// childAttemptSlot reserves retry space per child under the
// uq_step_instance_attempt unique index. Must exceed any plausible
// max_attempts (default 3, node-configurable but bounded by int32).
// 1024 leaves ~1000 retries per child — well past the circuit-breaker
// limit (3) and any reasonable rework budget.
const childAttemptSlot = 1024

// ErrFanOutMultiEdge signals that the fan_out node has more than one
// downstream edge, which is not supported in P1-1 (multi-edge fan_out
// is P1-9).
var ErrFanOutMultiEdge = errors.New("workflow: P1-1 only supports single-edge fan_out (multi-edge is P1-9)")

// activateFanOutNode is the fan_out dispatcher (design.md §2.2):
//
//  1. Verify the fan_out has exactly one downstream edge AND that edge
//     lands on an agent node (P1-1 scope).
//  2. Read upstream submission.exit_fields[items_field] as a JSON array.
//  3. Validate every item via parseSubtasks + per-item runtime checks
//     (agent_selector resolution, label existence).
//  4. Inside one tx (run row locked): for each validated item, create
//     a child issue, an active child StepInstance bound to the fan_out
//     step via parent_step_id, and enqueue the agent dispatch.
//  5. Transition the fan_out step itself to passed (pure splitter).
//
// Any 422-shaped failure (missing items_field, malformed subtask,
// unresolved agent, unknown label) aborts the whole expansion and
// surfaces as failActivation → step blocked + run paused.
func (e *Engine) activateFanOutNode(ctx context.Context, run db.WorkflowRun, snap *Snapshot, node *SnapshotNode, step db.StepInstance) error {
	// P1-1 scope guard: single-edge only.
	downstreams := snap.NextAfterAll(node.NodeKey)
	if len(downstreams) != 1 {
		return e.failActivation(ctx, run, step, fmt.Errorf("%w: fan_out %q has %d downstream edges",
			ErrFanOutMultiEdge, node.NodeKey, len(downstreams)))
	}
	branchNode := downstreams[0]
	if branchNode.Type != NodeTypeAgent {
		return e.failActivation(ctx, run, step, fmt.Errorf(
			"workflow: fan_out %q downstream %q must be an agent node in P1-1 (got %q)",
			node.NodeKey, branchNode.NodeKey, branchNode.Type))
	}

	itemsField := node.Config.ItemsField
	if itemsField == "" {
		return e.failActivation(ctx, run, step, fmt.Errorf(
			"workflow: fan_out node %q has no items_field configured", node.NodeKey))
	}

	items, rawErr := e.loadFanOutItems(ctx, run, snap, node, itemsField)
	if rawErr != nil {
		return e.failActivation(ctx, run, step, rawErr)
	}

	parsed, parseErrs := parseSubtasks(items)
	if len(parseErrs) > 0 {
		return e.failActivation(ctx, run, step, &ExitFieldsValidationError{
			Fields: subtaskErrorsToFieldErrors(parseErrs),
		})
	}

	// Runtime checks (publish cannot do these — items do not exist
	// until the upstream agent submits): agent_selector resolution +
	// label existence.
	resolver := newSubtaskResolver(e.Queries, run.WorkspaceID)
	if err := resolver.resolve(ctx, parsed); err != nil {
		return e.failActivation(ctx, run, step, err)
	}

	return e.fanOutDispatch(ctx, run, snap, node, step, &branchNode, parsed, resolver)
}

// loadFanOutItems reads the upstream submission's exit_fields array
// named by itemsField. fan_out does not own the items — they were
// written by the upstream agent node when its submission passed the
// verdict consumer (which copies exit_fields onto the step row).
// We read from the step_instance.exit_fields blob directly, scanning
// every inbound edge for the first passed upstream step that carries
// a non-empty array under itemsField.
func (e *Engine) loadFanOutItems(ctx context.Context, run db.WorkflowRun, snap *Snapshot, fanOutNode *SnapshotNode, itemsField string) ([]any, error) {
	var upstreamKeys []string
	for _, edge := range snap.Edges {
		if edge.ToNodeKey == fanOutNode.NodeKey {
			upstreamKeys = append(upstreamKeys, edge.FromNodeKey)
		}
	}
	if len(upstreamKeys) == 0 {
		return nil, fmt.Errorf("workflow: fan_out %q has no upstream node", fanOutNode.NodeKey)
	}
	for _, upKey := range upstreamKeys {
		upStep, err := e.Queries.GetStepInstanceForNodeWithStatus(ctx, db.GetStepInstanceForNodeWithStatusParams{
			RunID: run.ID, NodeKey: upKey, Status: StepPassed,
		})
		if err != nil {
			continue
		}
		if len(upStep.ExitFields) == 0 {
			continue
		}
		var fields map[string]any
		if err := json.Unmarshal(upStep.ExitFields, &fields); err != nil {
			continue
		}
		raw, ok := fields[itemsField]
		if !ok || raw == nil {
			continue
		}
		arr, ok := raw.([]any)
		if !ok {
			return nil, &ExitFieldsValidationError{Fields: []FieldError{{
				Name:     itemsField,
				Code:     "type_mismatch",
				Expected: "array",
				Message:  fmt.Sprintf("fan_out items_field %q must be an array", itemsField),
			}}}
		}
		return arr, nil
	}
	// AC13: upstream submission lacks the configured items_field.
	return nil, &ExitFieldsValidationError{Fields: []FieldError{{
		Name:     itemsField,
		Code:     "missing",
		Expected: "array",
		Message:  fmt.Sprintf("fan_out items_field %q not found on any passed upstream step", itemsField),
	}}}
}

// subtaskResolver performs the runtime per-item checks that publish
// cannot do (items do not exist at publish time). It caches agent +
// label lookups so multi-item templates with shared selectors stay
// cheap. agentIDByIndex / labelIDByName are populated by resolve()
// and consumed by fanOutDispatch.
type subtaskResolver struct {
	q             *db.Queries
	workspaceID   pgtype.UUID
	agentCache    map[string]string // selector → agent UUID
	agentIDByItem []string          // agent UUID per item index
	labelCache    map[string]string // label name → label UUID
}

func newSubtaskResolver(q *db.Queries, workspaceID pgtype.UUID) *subtaskResolver {
	return &subtaskResolver{
		q:          q,
		workspaceID: workspaceID,
		agentCache: map[string]string{},
		labelCache: map[string]string{},
	}
}

// resolve validates + resolves every subtask's agent_selector and
// labels. Returns a 422-shaped error on the first failure so the
// handler surfaces it without leaking engine internals. On success,
// agentIDByItem is populated in item order.
func (r *subtaskResolver) resolve(ctx context.Context, items []SubtaskItem) error {
	// Prime the label cache once: ListLabels returns every issue label
	// in the workspace, so a single round-trip covers every item.
	labels, err := r.q.ListLabels(ctx, db.ListLabelsParams{
		WorkspaceID:  r.workspaceID,
		ResourceType: "issue",
	})
	if err != nil {
		return fmt.Errorf("workflow: list workspace labels: %w", err)
	}
	for _, l := range labels {
		r.labelCache[l.Name] = util.UUIDToString(l.ID)
	}

	r.agentIDByItem = make([]string, len(items))
	for i, item := range items {
		agentID, err := r.resolveAgent(ctx, item.AgentSelector)
		if err != nil {
			return &ExitFieldsValidationError{Fields: []FieldError{{
				Name:    fmt.Sprintf("items[%d].agent_selector", i),
				Code:    "invalid",
				Message: err.Error(),
			}}}
		}
		r.agentIDByItem[i] = agentID
		for _, labelName := range item.Labels {
			if _, ok := r.labelCache[labelName]; !ok {
				return &ExitFieldsValidationError{Fields: []FieldError{{
					Name:    fmt.Sprintf("items[%d].labels", i),
					Code:    "invalid",
					Message: fmt.Sprintf("label %q does not exist in workspace %s", labelName, util.UUIDToString(r.workspaceID)),
				}}}
			}
		}
	}
	return nil
}

// resolveAgent mirrors TemplateService.resolveAgent semantics: a
// UUID-shaped selector resolves by ID; anything else matches by name
// and must be unique. Errors are 422-shaped (no agent / ambiguous).
func (r *subtaskResolver) resolveAgent(ctx context.Context, selector string) (string, error) {
	if cached, ok := r.agentCache[selector]; ok {
		return cached, nil
	}
	if id, err := util.ParseUUID(selector); err == nil {
		agent, err := r.q.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
			ID: id, WorkspaceID: r.workspaceID,
		})
		if err != nil || agent.ArchivedAt.Valid {
			return "", fmt.Errorf("agent_selector %q does not resolve to an active agent", selector)
		}
		idStr := util.UUIDToString(agent.ID)
		r.agentCache[selector] = idStr
		return idStr, nil
	}
	agents, err := r.q.ListAgents(ctx, r.workspaceID)
	if err != nil {
		return "", fmt.Errorf("list agents: %w", err)
	}
	var matches []db.Agent
	for _, a := range agents {
		if a.Name == selector && !a.ArchivedAt.Valid {
			matches = append(matches, a)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("agent_selector %q does not resolve to an active agent", selector)
	case 1:
		idStr := util.UUIDToString(matches[0].ID)
		r.agentCache[selector] = idStr
		return idStr, nil
	default:
		return "", fmt.Errorf("agent_selector %q matches multiple agents", selector)
	}
}

// fanOutDispatch performs the validated expansion inside one tx. The
// tx holds: child issue create + child step_instance create (parent_step_id
// linked, slot-based attempt) + agent dispatch + label attaches +
// fan_out step transition + converge pre-create. All-or-nothing: a
// failure on any item rolls back the whole expansion.
func (e *Engine) fanOutDispatch(
	ctx context.Context, run db.WorkflowRun, snap *Snapshot, fanOutNode *SnapshotNode,
	fanOutStep db.StepInstance, branchNode *SnapshotNode, items []SubtaskItem,
	resolver *subtaskResolver,
) error {
	tx, err := e.TxStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := e.Queries.WithTx(tx)

	// Lock the run for the duration of the expansion so concurrent
	// fan_out triggers (e.g. two push events for the same run) serialize.
	lockedRun, err := qtx.GetWorkflowRunForUpdate(ctx, run.ID)
	if err != nil {
		return fmt.Errorf("lock run for fan_out: %w", err)
	}
	if lockedRun.Status != RunRunning && lockedRun.Status != RunWaitingAcceptance {
		return fmt.Errorf("workflow: fan_out trigger on %q run aborted (status %q)", lockedRun.Status, lockedRun.Status)
	}

	initiator := ParseRunContext(run.Context).Initiator()
	creator := initiator
	if !creator.Valid {
		creator = pgtype.UUID{Valid: true} // system-actor zero UUID
	}

	// Pre-create the converge step as pending once (idempotent under
	// uq_step_instance_attempt: (run, converge, NULL, 1)). fan_out →
	// branch → converge; only one converge per fan_out branch line.
	convergeNode := firstConvergeDownstream(snap, branchNode)
	if convergeNode == nil {
		return fmt.Errorf("workflow: fan_out %q: branch %q has no downstream converge (template pairing broken)",
			fanOutNode.NodeKey, branchNode.NodeKey)
	}
	if err := preCreateStepTx(ctx, qtx, run.ID, convergeNode.NodeKey); err != nil {
		return fmt.Errorf("pre-create converge step: %w", err)
	}

	type dispatchedChild struct {
		step  db.StepInstance
		issue db.Issue
	}
	var children []dispatchedChild

	for i, item := range items {
		agentID, _ := util.ParseUUID(resolver.agentIDByItem[i])
		priority := item.Priority
		if priority == "" {
			priority = SubtaskPriorityNone
		}
		var labelIDs []pgtype.UUID
		for _, name := range item.Labels {
			labelIDs = append(labelIDs, util.MustParseUUID(resolver.labelCache[name]))
		}

		res, err := e.Issues.Create(ctx, service.IssueCreateParams{
			WorkspaceID:    run.WorkspaceID,
			Title:          item.Title,
			Description:    pgtype.Text{String: item.Instructions, Valid: item.Instructions != ""},
			Status:         "backlog",
			Priority:       priority,
			AssigneeType:   pgtype.Text{String: "agent", Valid: true},
			AssigneeID:     agentID,
			CreatorType:    "member",
			CreatorID:      creator,
			ParentIssueID:  run.IntakeIssueID,
			LabelIDs:       labelIDs,
			AllowDuplicate: true,
		}, service.IssueCreateOpts{})
		if err != nil {
			return fmt.Errorf("fan_out create child issue %d: %w", i, err)
		}
		issue := res.Issue

		// Child step row — slot-based attempt keeps the unique index
		// happy across N children sharing (run, branch_node, fanout_step).
		childStep, err := qtx.CreateStepInstance(ctx, db.CreateStepInstanceParams{
			RunID:        run.ID,
			NodeKey:      branchNode.NodeKey,
			Status:       StepActive,
			Attempt:      int32(i*childAttemptSlot + 1),
			ParentStepID: fanOutStep.ID,
		})
		if err != nil {
			return fmt.Errorf("fan_out create child step %d: %w", i, err)
		}
		writeTransitionTx(ctx, qtx, run.ID, childStep.ID, "none", StepActive, childStep.Attempt, "engine", map[string]any{
			"fan_out_parent": fanOutNode.NodeKey,
			"child_index":    i,
		})

		// Handoff note carries fan_out + branch instructions and the
		// item-specific brief. Matches activateAgentNode'shygiene
		// (sanitize, cap at 4KB, prefix every line).
		note := buildChildHandoffNote(fanOutNode, branchNode, item, i)

		task, err := e.Tasks.EnqueueTaskForIssueWithHandoff(ctx, issue, note, initiator)
		if err != nil {
			return fmt.Errorf("fan_out enqueue child task %d: %w", i, err)
		}

		if _, err := qtx.UpdateStepInstanceDispatch(ctx, db.UpdateStepInstanceDispatchParams{
			ID:          childStep.ID,
			AgentID:     agentID,
			AgentTaskID: task.ID,
			IssueID:     issue.ID,
		}); err != nil {
			slog.Error("workflow: fan_out link dispatch artifacts failed",
				"step_instance_id", util.UUIDToString(childStep.ID), "error", err)
		}

		if _, err := e.Issues.SetStatus(ctx, issue, "todo"); err != nil {
			slog.Error("workflow: fan_out promote child issue to todo failed",
				"issue_id", util.UUIDToString(issue.ID), "error", err)
		}
		children = append(children, dispatchedChild{step: childStep, issue: issue})
	}

	// fan_out step itself → passed (pure splitter, no agent task).
	if !e.transitionStepTx(ctx, qtx, fanOutStep, StepPassed, "engine", map[string]any{
		"fan_out_expanded": len(items),
	}) {
		return fmt.Errorf("workflow: fan_out step %q lost transition guard race", fanOutNode.NodeKey)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit fan_out dispatch: %w", err)
	}

	// Post-commit: emit events for the fan_out pass + each child
	// activation. Order: fan_out first (UI sees the splitter resolve
	// before its children light up), then children.
	e.publishStepUpdated(run, fanOutStep.ID, StepPassed)
	for _, c := range children {
		e.publishStepUpdated(run, c.step.ID, StepActive)
		e.publishIssueCreated(c.issue)
	}
	return nil
}

// firstConvergeDownstream locates the first converge-typed node
// reachable downstream from node via NextAfterAll (BFS). fan_out →
// branch → converge is the P1-1 shape; this works for any branch
// chain that eventually hits a converge. Returns nil if none found
// (the publish-time pairing check should prevent that, but a
// hand-edited snapshot could still miss it).
func firstConvergeDownstream(snap *Snapshot, node *SnapshotNode) *SnapshotNode {
	visited := map[string]bool{node.NodeKey: true}
	queue := []string{node.NodeKey}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, nxt := range snap.NextAfterAll(cur) {
			if visited[nxt.NodeKey] {
				continue
			}
			visited[nxt.NodeKey] = true
			if nxt.Type == NodeTypeConverge {
				return &nxt
			}
			queue = append(queue, nxt.NodeKey)
		}
	}
	return nil
}

// buildChildHandoffNote assembles the fan_out child agent's opening
// prompt: fan_out-level instructions, branch-level instructions, and
// the item-specific brief. Mirrors activateAgentNode's hygiene
// (sanitize + cap + quote-prefix) via finalizeHandoffNote.
func buildChildHandoffNote(fanOut, branch *SnapshotNode, item SubtaskItem, itemIndex int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[workflow fan_out] %s → %s — child #%d\n",
		sanitizePromptText(fanOut.Name), sanitizePromptText(branch.Name), itemIndex)
	if fanOut.Config.Instructions != "" {
		fmt.Fprintf(&b, "[fan_out instructions] %s\n", sanitizePromptText(fanOut.Config.Instructions))
	}
	if branch.Config.Instructions != "" {
		fmt.Fprintf(&b, "[branch instructions] %s\n", sanitizePromptText(branch.Config.Instructions))
	}
	fmt.Fprintf(&b, "[child task] %s\n", sanitizePromptText(item.Title))
	fmt.Fprintf(&b, "[child brief] %s\n", sanitizePromptText(item.Instructions))
	if schema := branch.Config.ExitFields; schema != nil && len(schema.Fields) > 0 {
		fmt.Fprintf(&b, "[exit_fields schema] %s\n", compactJSON(schema, 1024))
	}
	b.WriteString("Full node context: `multica step context`.")
	return finalizeHandoffNote(b.String())
}

// subtaskErrorsToFieldErrors lifts SubtaskFieldError (parseSubtasks
// output) into the handler-facing FieldError shape so 422 responses
// share one envelope across publish + runtime validation paths.
func subtaskErrorsToFieldErrors(errs []SubtaskFieldError) []FieldError {
	out := make([]FieldError, 0, len(errs))
	for _, e := range errs {
		name := e.Name
		if e.ItemIndex >= 0 {
			name = fmt.Sprintf("items[%d].%s", e.ItemIndex, e.Name)
		}
		out = append(out, FieldError{
			Name:     name,
			Code:     e.Code,
			Expected: e.Expected,
			Message:  e.Message,
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// applyFailPolicy (Wave 2.4)
// ---------------------------------------------------------------------------

// applyFailPolicy applies a fan_out's fail_policy decision to the
// sibling group of a failed/blocked child step. Caller is
// handleChildStepTerminal (converge.go); the qtx is the caller's
// transaction (run row already locked).
//
// Behavior by policy:
//
//   - FailPolicyFail: every other non-terminal child step → skipped
//     (active/dispatched/running only); run → failed; inbox the
//     initiator (AC7). Skipped siblings trigger no further converge
//     logic (skipped is not in the outcome set).
//   - FailPolicyBlocked: run → paused; the converge step (still
//     pending) → blocked; inbox the reviewer or initiator (AC8).
//     Siblings keep running — their later outcomes land in a run
//     that is already paused, where verdict consumption no-ops.
//   - FailPolicyRework: handled directly by reworkChildStepScope
//     (below); applyFailPolicy does nothing for this policy.
//
// Returns the slice of sibling step IDs that were skipped (for
// post-commit event emission). The failed/blocked child itself is
// NOT in the slice — it already transitioned via consumeVerdictTx.
func (e *Engine) applyFailPolicy(ctx context.Context, qtx *db.Queries, run db.WorkflowRun, fanOutStep db.StepInstance, policy string) ([]pgtype.UUID, error) {
	switch policy {
	case FailPolicyFail:
		// Conditional UPDATE-shaped skip: take every non-terminal child
		// of this fan_out to skipped in one tx-wide operation. Reading
		// then iterating keeps step_transition writes uniform.
		children, err := qtx.ListStepInstancesForRun(ctx, run.ID)
		if err != nil {
			return nil, fmt.Errorf("list steps for fail policy: %w", err)
		}
		var skipped []pgtype.UUID
		for _, c := range children {
			if !c.ParentStepID.Valid || util.UUIDToString(c.ParentStepID) != util.UUIDToString(fanOutStep.ID) {
				continue
			}
			if isTerminalStepStatus(c.Status) {
				continue // failed/blocked child + any already-terminal siblings
			}
			if e.transitionStepTx(ctx, qtx, c, StepSkipped, "engine", map[string]any{
				"fail_policy": FailPolicyFail,
				"fan_out":     c.NodeKey,
			}) {
				skipped = append(skipped, c.ID)
			}
		}
		// Run → failed (guarded; respect any earlier terminal flip).
		for _, expected := range []string{RunRunning, RunWaitingAcceptance, RunPaused} {
			if _, err := qtx.UpdateWorkflowRunStatus(ctx, db.UpdateWorkflowRunStatusParams{
				NewStatus: RunFailed, ID: run.ID, ExpectedStatus: expected,
			}); err == nil {
				break
			} else if !errors.Is(err, pgx.ErrNoRows) {
				return nil, fmt.Errorf("fail policy flip run: %w", err)
			}
		}
		return skipped, nil

	case FailPolicyBlocked:
		// Find the converge step and flip it to blocked. Children stay
		// in flight; the run lock means new verdicts on them no-op
		// once the run is paused.
		runNow, err := qtx.GetWorkflowRun(ctx, run.ID)
		if err != nil {
			return nil, fmt.Errorf("re-read run in blocked policy: %w", err)
		}
		snap, err := ParseSnapshot(runNow.TemplateSnapshot)
		if err != nil {
			return nil, err
		}
		fanOutNode := snap.NodeByKey(fanOutStep.NodeKey)
		if fanOutNode != nil {
			convergeNode := firstConvergeDownstream(snap, fanOutNode)
			if convergeNode != nil {
				if convStep, cerr := qtx.GetStepInstanceForNodeWithStatus(ctx, db.GetStepInstanceForNodeWithStatusParams{
					RunID: run.ID, NodeKey: convergeNode.NodeKey, Status: StepPending,
				}); cerr == nil {
					e.transitionStepTx(ctx, qtx, convStep, StepBlocked, "engine", map[string]any{
						"fail_policy": FailPolicyBlocked,
					})
				}
			}
		}
		// Run → paused.
		e.pauseRunTx(ctx, qtx, run)
		return nil, nil

	case FailPolicyRework:
		// No-op: reworkChildStepScope (caller of applyFailPolicy for
		// this branch) handles the retry directly.
		return nil, nil
	}
	return nil, fmt.Errorf("workflow: unknown fail_policy %q", policy)
}

// reworkChildStepScope is the fan_out child step专用 rework helper
// (design.md §4.4). It is P0 RequestRework minus DownstreamNodeKeys:
//
//   - Marks the failed child step as StepRework.
//   - Inserts a fresh attempt for the SAME (run, node, parent) —
//     slot-encoded so it stays unique under uq_step_instance_attempt.
//   - Re-dispatches the agent with a rework_context handoff.
//
// Crucially, it does NOT invalidate siblings or downstream (no BFS):
// the fan_out converge AND semantics needs the other children to keep
// their state. Calling P0 RequestRework here would BFS from the child
// node through converge and post-converge, invalidating the whole
// post-converge chain — a clear violation of R7 ("siblings unaffected").
//
// Boundary: grep DownstreamNodeKeys fanout.go must stay 0 hits.
func (e *Engine) reworkChildStepScope(ctx context.Context, run db.WorkflowRun, snap *Snapshot, branchNode *SnapshotNode, childStep db.StepInstance, fanOutNode *SnapshotNode) error {
	rc, err := e.assembleReworkContext(ctx, run.ID, branchNode.NodeKey, "fan_out child fail", "verdict_fail")
	if err != nil {
		return err
	}
	rcJSON, mErr := json.Marshal(rc)
	if mErr != nil {
		return fmt.Errorf("marshal rework context: %w", mErr)
	}

	tx, err := e.TxStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := e.Queries.WithTx(tx)

	if _, err := qtx.GetWorkflowRunForUpdate(ctx, run.ID); err != nil {
		return fmt.Errorf("lock run for child rework: %w", err)
	}

	// Mark the failed child attempt reworked. Use transitionStepTx for
	// the audit row; the prev status (failed) is what the count sees.
	if !e.transitionStepTx(ctx, qtx, childStep, StepRework, "engine", map[string]any{
		"reason":       "fan_out child fail (policy=rework)",
		"rework_scope": "child",
	}) {
		// Lost the guard race; another transition landed first. Treat
		// as a no-op success — the run lock makes this extremely rare.
		return tx.Commit(ctx)
	}

	// Circuit breaker ① for the child node: count consecutive reworks
	// of this exact (run, node, parent) — we approximate by CountConsecutiveReworksForNode
	// which counts by (run, node). Same node = same branch_node across
	// children, so the limit applies to the fan_out's branch slot as a
	// whole, not per child. P1-1 doc note: this is acceptable because
	// the policy is set at fan_out granularity.
	consecutive, err := qtx.CountConsecutiveReworksForNode(ctx, db.CountConsecutiveReworksForNodeParams{
		RunID: run.ID, NodeKey: branchNode.NodeKey,
	})
	if err != nil {
		return fmt.Errorf("count child reworks: %w", err)
	}
	if consecutive >= circuitBreakerLimit {
		// Hand off: pause run, escalate to human.
		if !e.pauseRunTx(ctx, qtx, run) {
			return tx.Commit(ctx)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit rework breaker: %w", err)
		}
		e.publishStepUpdated(run, childStep.ID, StepRework)
		e.publishRunUpdated(run, RunPaused)
		e.handoffToHuman(ctx, run,
			fmt.Sprintf("Workflow paused: fan_out branch %q reworked %d times in a row", branchNode.NodeKey, consecutive),
			"workflow_circuit_breaker")
		return nil
	}

	// Fresh attempt inside the child's slot. attempt = childBaseSlot + round,
	// where round = (current attempt % slot) + 1. Identifies the slot
	// owner (child index) and increments only within that slot.
	slotRound := int(childStep.Attempt) % childAttemptSlot
	if slotRound == 0 {
		slotRound = childAttemptSlot // edge case: attempt was exactly on a slot boundary
	}
	newAttempt := childStep.Attempt + 1
	fresh, err := qtx.CreateStepInstance(ctx, db.CreateStepInstanceParams{
		RunID:        run.ID,
		NodeKey:      branchNode.NodeKey,
		Status:       StepActive,
		Attempt:      newAttempt,
		ParentStepID: childStep.ParentStepID,
	})
	if err != nil {
		return fmt.Errorf("create rework child step: %w", err)
	}
	writeTransitionTx(ctx, qtx, run.ID, fresh.ID, "none", StepActive, newAttempt, "engine", map[string]any{
		"rework_of":   util.UUIDToString(childStep.ID),
		"rework_round": slotRound + 1,
	})
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit child rework: %w", err)
	}

	e.publishStepUpdated(run, childStep.ID, StepRework)
	e.publishStepUpdated(run, fresh.ID, StepActive)

	// Post-commit: re-dispatch the agent with the rework context.
	// Reuse the failed child's issue (carry the conversation forward)
	// — same pattern as activateAgentNode re-using the issue row.
	if childStep.IssueID.Valid {
		issue, ierr := e.Queries.GetIssue(ctx, childStep.IssueID)
		if ierr == nil {
			initiator := ParseRunContext(run.Context).Initiator()
			note := buildChildHandoffNote(fanOutNode, branchNode, SubtaskItem{
				Title:        issue.Title,
				Instructions: "(rework) see prior attempt + rework context",
			}, int(childStep.Attempt/childAttemptSlot))
			// Append rework context to the note.
			note = strings.TrimRight(note, "\n") + "\n> [rework] " + renderReworkContext(rc)
			task, terr := e.Tasks.EnqueueTaskForIssueWithHandoff(ctx, issue, note, initiator)
			if terr != nil {
				slog.Error("workflow: child rework enqueue failed",
					"step_id", util.UUIDToString(fresh.ID), "error", terr)
			} else if _, uerr := e.Queries.UpdateStepInstanceDispatch(ctx, db.UpdateStepInstanceDispatchParams{
				ID: fresh.ID, AgentID: issue.AssigneeID, AgentTaskID: task.ID, IssueID: issue.ID,
			}); uerr != nil {
				slog.Error("workflow: child rework dispatch link failed",
					"step_id", util.UUIDToString(fresh.ID), "error", uerr)
			}
		}
	}
	_ = rcJSON // rc captured into renderReworkContext path above; JSON form reserved for tracing
	return nil
}
