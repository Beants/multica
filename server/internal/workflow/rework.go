package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// rework.go — targeted rework (design.md §4.4) and prompt-injection hygiene
// (§4.2). RequestRework re-enters a node with a fresh attempt, invalidates
// ALL downstream non-skipped steps (including already-passed ones — R3
// review #4), and carries a sanitized rework_context into the next handoff
// note (D-8 explicit injection).

// handoffNoteMaxBytes caps the injected note (§4.2 hard constraint: the note
// is unbounded TEXT in the DB but the opening prompt must stay small; the
// overflow pointer sends the agent to `multica step context` for the rest).
const handoffNoteMaxBytes = 4096

// ReworkContext travels from the rejection to the target node's next
// handoff note (and is persisted on acceptance.rework_context).
type ReworkContext struct {
	Reason        string               `json:"reason"`
	TargetNodeKey string               `json:"target_node_key"`
	Source        string               `json:"source"` // acceptance_reject | verdict_fail
	History       []ReworkVerdictEntry `json:"history,omitempty"`
}

// ReworkVerdictEntry is one prior verdict on the rework target, newest last.
type ReworkVerdictEntry struct {
	Attempt   int32  `json:"attempt"`
	Result    string `json:"result"`
	RootCause string `json:"root_cause,omitempty"`
	By        string `json:"by"`
}

// reworkHistoryLimit bounds the verdict history embedded in the context so
// the note stays inside the 4KB budget.
const reworkHistoryLimit = 5

// RequestRework re-enters targetNodeKey with a fresh attempt (design.md
// §4.4): the target's latest terminal step is marked rework, every
// downstream non-skipped step (pending through passed) is skipped with a
// step_transition, the consecutive-rework circuit breaker is evaluated, and
// — when the breaker stays quiet — the target activates again with the
// rework context in its handoff note.
func (e *Engine) RequestRework(ctx context.Context, runID pgtype.UUID, targetNodeKey string, rc *ReworkContext) error {
	if rc == nil {
		assembled, err := e.assembleReworkContext(ctx, runID, targetNodeKey, "", "verdict_fail")
		if err != nil {
			return err
		}
		rc = assembled
	}

	type skippedStep struct{ step db.StepInstance }
	var (
		run     db.WorkflowRun
		snap    *Snapshot
		target  *SnapshotNode
		oldStep db.StepInstance
		newStep db.StepInstance
		skipped []skippedStep
	)

	tx, err := e.TxStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := e.Queries.WithTx(tx)

	run, err = qtx.GetWorkflowRunForUpdate(ctx, runID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrRunNotFound
		}
		return fmt.Errorf("lock run: %w", err)
	}
	if run.Status != RunRunning && run.Status != RunWaitingAcceptance {
		return ErrRunNotActive
	}
	snap, err = ParseSnapshot(run.TemplateSnapshot)
	if err != nil {
		return err
	}
	target = snap.NodeByKey(targetNodeKey)
	if target == nil {
		return ErrReworkTargetUnknown
	}
	latest, err := qtx.GetLatestStepInstanceForNode(ctx, db.GetLatestStepInstanceForNodeParams{
		RunID: runID, NodeKey: targetNodeKey,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrReworkTargetUnknown
		}
		return fmt.Errorf("read target step: %w", err)
	}
	if !isTerminalStepStatus(latest.Status) {
		// 验收驳回 vs 进行中 step (blueprint §8.1): refusing is safer than
		// merging — the in-flight attempt would lose its verdict landing.
		return ErrReworkTargetActive
	}
	oldStep = latest

	// Mark the target's current attempt reworked (transition feeds the
	// consecutive-rework counter, so it must precede the count).
	if !e.transitionStepTx(ctx, qtx, oldStep, StepRework, "human", map[string]any{"reason": rc.Reason}) {
		return nil // lost the guard race; another transition landed first
	}

	// Circuit breaker ①: consecutive reworks of this node since its last
	// pass (design.md §4.4). The transition above makes this count include
	// the current round.
	consecutive, err := qtx.CountConsecutiveReworksForNode(ctx, db.CountConsecutiveReworksForNodeParams{
		RunID: runID, NodeKey: targetNodeKey,
	})
	if err != nil {
		return fmt.Errorf("count consecutive reworks: %w", err)
	}
	if consecutive >= circuitBreakerLimit {
		if !e.pauseRunTx(ctx, qtx, run) {
			return nil
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit: %w", err)
		}
		e.publishStepUpdated(run, oldStep.ID, StepRework)
		e.publishRunUpdated(run, RunPaused)
		e.handoffToHuman(ctx, run,
			fmt.Sprintf("Workflow paused: node %q reworked %d times in a row", targetNodeKey, consecutive),
			"workflow_circuit_breaker")
		return nil
	}

	// Downstream invalidation (R3 review #4): EVERY non-skipped step after
	// the target — passed ones included — is skipped so the re-run passes
	// the gates again before reaching acceptance (AC2).
	for _, downKey := range snap.DownstreamNodeKeys(targetNodeKey) {
		down, err := qtx.GetLatestStepInstanceForNode(ctx, db.GetLatestStepInstanceForNodeParams{
			RunID: runID, NodeKey: downKey,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return fmt.Errorf("read downstream step %q: %w", downKey, err)
		}
		if down.Status == StepSkipped {
			continue
		}
		if !e.transitionStepTx(ctx, qtx, down, StepSkipped, "engine", map[string]any{"rework_target": targetNodeKey}) {
			continue // raced to a terminal state by its own consumer
		}
		skipped = append(skipped, skippedStep{step: down})
	}

	// The target re-enters with a fresh attempt. The node after it is NOT
	// pre-created here: its previous pending row was just skipped, and the
	// standard pass path re-creates it when the target passes again — that
	// keeps the downstream-invalidation state observable until re-pass.
	newStep, err = newAttemptStepTx(ctx, qtx, runID, targetNodeKey, oldStep.Attempt+1)
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	// WS fanout for the committed transitions (events.go emission rule).
	e.publishStepUpdated(run, oldStep.ID, StepRework)
	for _, s := range skipped {
		e.publishStepUpdated(run, s.step.ID, StepSkipped)
	}
	e.publishStepUpdated(run, newStep.ID, StepActive)

	// Post-commit side effects: retire the old attempt's child issue and any
	// invalidated downstream issues, then dispatch the fresh attempt with the
	// rework context injected (D-8).
	e.closeStepIssue(ctx, oldStep, "cancelled")
	for _, s := range skipped {
		e.closeStepIssue(ctx, s.step, "cancelled")
	}
	return e.activateNode(ctx, run, snap, target, newStep, rc)
}

// assembleReworkContext builds the ReworkContext for a target node from the
// run's verdict history (§4.2: step_transition + verdict history; here the
// verdict rows joined through their steps carry everything the note needs).
func (e *Engine) assembleReworkContext(ctx context.Context, runID pgtype.UUID, targetNodeKey, reason, source string) (*ReworkContext, error) {
	rc := &ReworkContext{Reason: reason, TargetNodeKey: targetNodeKey, Source: source}
	steps, err := e.Queries.ListStepInstancesForRun(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("list steps: %w", err)
	}
	nodeByStepID := map[string]string{}
	attemptByStepID := map[string]int32{}
	for _, s := range steps {
		nodeByStepID[util.UUIDToString(s.ID)] = s.NodeKey
		attemptByStepID[util.UUIDToString(s.ID)] = s.Attempt
	}
	verdicts, err := e.Queries.ListVerdictsForRun(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("list verdicts: %w", err)
	}
	for _, v := range verdicts {
		if nodeByStepID[util.UUIDToString(v.StepInstanceID)] != targetNodeKey {
			continue
		}
		entry := ReworkVerdictEntry{
			Attempt: attemptByStepID[util.UUIDToString(v.StepInstanceID)],
			Result:  v.Result,
			By:      v.VerdictBy,
		}
		if v.RootCause.Valid {
			entry.RootCause = v.RootCause.String
		}
		rc.History = append(rc.History, entry)
	}
	if len(rc.History) > reworkHistoryLimit {
		rc.History = rc.History[len(rc.History)-reworkHistoryLimit:]
	}
	return rc, nil
}

// ---------------------------------------------------------------------------
// Handoff note construction + sanitization (design.md §4.2)
// ---------------------------------------------------------------------------

// buildHandoffNote assembles the node context injected into the task's
// opening prompt via EnqueueTaskForIssueWithHandoff (zero-touch mechanism,
// §7 deviation #3): node instructions + upstream exit_fields summary + this
// node's exit-fields schema [+ rework_context on rework rounds]. The note
// is sanitized and capped at 4KB; every line is prefixed so it stays inside
// prompt.go's `> ` quote frame (which only prefixes the first line itself).
//
// P1-3b adversarial gate form (squad-briefing.md:158): when adversarial is
// true, the note is whitelisted to node identity + exit_fields schema only.
// Instructions, upstream exit_fields, and rework context are skipped so the
// reviewer cannot lean on the builder's framing — it must derive its
// verdict from the diff/test cases the daemon exposes via the workdir.
func (e *Engine) buildHandoffNote(ctx context.Context, run db.WorkflowRun, snap *Snapshot, node *SnapshotNode, step db.StepInstance, rc *ReworkContext, adversarial bool, agentID pgtype.UUID) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[workflow node] %s (%s) — attempt %d\n", sanitizePromptText(node.Name), node.NodeKey, step.Attempt)

	if adversarial {
		// Adversarial whitelist: identity + exit_fields schema only.
		// The daemon's workdir already exposes the diff/test cases —
		// those are the inputs we want the reviewer to reason from.
		if schema := node.Config.ExitFields; schema != nil && len(schema.Fields) > 0 {
			fmt.Fprintf(&b, "[exit_fields schema] %s\n", compactJSON(schema, 1024))
		}
		b.WriteString("Adversarial review: identify issues from the diff/test cases alone (no prd/design context).")
		return finalizeHandoffNote(b.String())
	}

	instructions := sanitizePromptText(node.Config.Instructions)
	if instructions == "" {
		instructions = "(no node instructions)"
	}
	fmt.Fprintf(&b, "[instructions] %s\n", instructions)

	// P1-4: inject soft context_inject rules bound to the dispatching agent
	// (team conventions the agent should honor). Best-effort — a lookup
	// failure never blocks dispatch; the note just omits the section.
	// Skipped for adversarial (that path returns early above with its strict
	// identity+schema whitelist).
	if agentID.Valid {
		if rules, err := e.Queries.ListSoftRulesForAgent(ctx, db.ListSoftRulesForAgentParams{
			WorkspaceID: run.WorkspaceID,
			TargetID:    agentID,
		}); err == nil {
			if rendered := renderSoftRules(rules); rendered != "" {
				fmt.Fprintf(&b, "[team rules] %s\n", rendered)
			}
		}
	}

	if up := upstreamNodeOf(snap, node.NodeKey); up != nil {
		if fields := e.passedExitFields(ctx, step.RunID, up.NodeKey); len(fields) > 0 {
			fmt.Fprintf(&b, "[upstream exit_fields from %s] %s\n", up.NodeKey, compactJSON(fields, 1024))
		}
	}

	if schema := node.Config.ExitFields; schema != nil && len(schema.Fields) > 0 {
		fmt.Fprintf(&b, "[exit_fields schema] %s\n", compactJSON(schema, 1024))
	}

	if rc != nil {
		fmt.Fprintf(&b, "[rework] %s\n", renderReworkContext(rc))
	}

	b.WriteString("Full node context: `multica step context`.")
	return finalizeHandoffNote(b.String())
}

// renderSoftRules flattens the soft context_inject rules bound to the agent
// into one sanitized line for the handoff note: "name: content; ...".
// Empty rules → "" (caller omits the [team rules] section).
func renderSoftRules(rules []db.WorkflowRule) string {
	var b strings.Builder
	for _, r := range rules {
		fmt.Fprintf(&b, "%s: %s; ", sanitizePromptText(r.Name), sanitizePromptText(r.Content))
	}
	return strings.TrimSuffix(b.String(), "; ")
}

// renderReworkContext flattens the rework context into one sanitized line.
func renderReworkContext(rc *ReworkContext) string {
	var b strings.Builder
	if rc.Reason != "" {
		fmt.Fprintf(&b, "reason: %s. ", rc.Reason)
	}
	for _, h := range rc.History {
		fmt.Fprintf(&b, "attempt %d: %s", h.Attempt, h.Result)
		if h.RootCause != "" {
			fmt.Fprintf(&b, " (%s)", h.RootCause)
		}
		b.WriteString("; ")
	}
	return sanitizePromptText(strings.TrimSuffix(b.String(), "; "))
}

// finalizeHandoffNote applies the §4.2 injection contract: every line gets
// a `> ` prefix (prompt.go only prefixes the first line itself, so
// unprepared multi-line content would escape the quote frame), and the
// whole note is truncated to the 4KB budget at a line boundary with a
// pointer to the full-fidelity CLI read.
func finalizeHandoffNote(raw string) string {
	lines := strings.Split(strings.TrimRight(raw, "\n"), "\n")
	var b strings.Builder
	b.Grow(len(raw) + 3*len(lines))
	for i, line := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("> ")
		b.WriteString(line)
	}
	note := b.String()
	if len(note) <= handoffNoteMaxBytes {
		return note
	}
	const overflow = "\n> …(truncated; run `multica step context` for the full node context)"
	budget := handoffNoteMaxBytes - len(overflow)
	// Cut at the last newline inside the budget; never split a UTF-8 rune.
	cut := strings.LastIndex(note[:budget], "\n")
	if cut < 0 {
		cut = budget
		for cut > 0 && !unicode.IsSpace(rune(note[cut])) {
			cut--
		}
	}
	return note[:cut] + overflow
}

// sanitizePromptText strips control characters and collapses whitespace so
// untrusted strings (instructions, reject reasons, root causes) cannot break
// the prompt's quote framing or smuggle terminal escapes (cairn rule, §4.2).
// Newlines fold into spaces — per-line structure is re-added by the caller.
func sanitizePromptText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	lastSpace := true // also trims leading whitespace
	for _, r := range s {
		switch {
		case unicode.IsSpace(r):
			if !lastSpace {
				b.WriteByte(' ')
				lastSpace = true
			}
		case unicode.IsControl(r):
			// Drop silently: \x00/\x07/\x1b and friends must not survive
			// into the prompt at all (escape-sequence smuggling).
		default:
			b.WriteRune(r)
			lastSpace = false
		}
	}
	return strings.TrimRight(b.String(), " ")
}

// compactJSON marshals v compactly and truncates the result to max bytes at
// a rune boundary (lossy but prompt-only; the full value lives in the DB).
func compactJSON(v any, max int) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return "(unavailable)"
	}
	if len(raw) > max {
		cut := max
		for cut > 0 && !utf8.RuneStart(raw[cut]) {
			cut--
		}
		raw = raw[:cut]
	}
	return string(raw)
}
