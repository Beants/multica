package workflow

// gate.go — P1-3 gate node MVP (script form only; PRD R5 + R6). gate is
// the second structural node type after acceptance that parks-or-advances
// the chain from its own activation, without an agent task in the middle.
// Unlike acceptance (waits for a human), gate runs an inline or
// workspace-registered script synchronously inside activateGateNode and
// derives the verdict from the script's last-line JSON.
//
// MVP scope (PRD §Goal):
//   - gate_type=script only. agent/rules/adversarial/hybrid forms return
//     ErrGateTypeNotImplemented; they pass publish validation so a
//     template carrying them survives until P1-3b activates them.
//   - synchronous server-side execution. The script runs in the server
//     process under an env allowlist (PATH/HOME/LANG/LC_ALL/TZ only);
//     daemon integration + secrets injection + network egress land in v2.
//
// Double-transaction boundary (design.md §txA/§txB, fixes review Issue 2):
//
//	txA (ms):  INSERT gate_run(status='running') + COMMIT
//	script:    exec.CommandContext(sh|python3 -c); NO tx held
//	txB (ms):  lock run, UPDATE gate_run(result), transitionStepTx,
//	           activateStepTx(downstream) OR pauseRunTx, COMMIT
//	post:      runSignalAction emits WS + advances downstream
//
// The split keeps the run row lock held for milliseconds even when the
// script itself runs for up to the gate_timeout_seconds cap (default 60s,
// global ceiling 300s).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// ErrGateTypeNotImplemented signals that a gate node's gate_type is one
// of the P1-3b forms (agent/rules/adversarial/hybrid) which the MVP
// activate path refuses to dispatch. The publish validator accepts these
// values so a template carrying them does not need a follow-up migration
// when P1-3b activates them; the runtime refusal is fail-visible.
var ErrGateTypeNotImplemented = errors.New("workflow: gate_type not implemented (only 'script' in P1-3 MVP; agent/rules/adversarial/hybrid land in P1-3b)")

// GateOutput is the parsed last-line JSON of a gate script's stdout
// (design.md §2.3). Status is the only strictly required field; the
// others are optional context that flows into the verdict payload.
type GateOutput struct {
	Status       string                   `json:"status"`                 // pass|block|warn
	FixHint      string                   `json:"fix_hint,omitempty"`     // sanitized before storage
	Facts        []string                 `json:"facts,omitempty"`        // objective observations
	Dispositions []map[string]interface{} `json:"dispositions,omitempty"` // structured per-item rulings
}

// GateRunOutput is the gate_run.output JSONB shape (design.md §2.3 +
// migration 926). Stdout/Stderr carry the raw script output (subject to
// max_output_bytes truncation); FixHint is the post-sanitize text ready
// for prompt re-injection; Facts/Dispositions mirror the parsed values.
type GateRunOutput struct {
	Stdout       string                   `json:"stdout,omitempty"`
	Stderr       string                   `json:"stderr,omitempty"`
	Truncated    bool                     `json:"truncated,omitempty"`
	FixHint      string                   `json:"fix_hint,omitempty"`
	Facts        []string                 `json:"facts,omitempty"`
	Dispositions []map[string]interface{} `json:"dispositions,omitempty"`
}

// gateExecResult is the raw execution output collected by executeScript
// (separate from GateOutput which is the PARSED last-line JSON, and from
// GateRunOutput which is the DB-shape JSONB). Carries stdout/stderr +
// the truncation flag derived from the limit writers.
type gateExecResult struct {
	stdout    string
	stderr    string
	truncated bool
}

// activateGateNode dispatches a gate node by gate_type. The MVP supports
// only gate_type=script; the other four forms return
// ErrGateTypeNotImplemented so the failure is visible (P1-3b activates
// them).
func (e *Engine) activateGateNode(ctx context.Context, run db.WorkflowRun, snap *Snapshot, node *SnapshotNode, step db.StepInstance) error {
	if node.Config.GateType != GateTypeScript {
		return e.failActivation(ctx, run, step, fmt.Errorf(
			"gate node %q: %w", node.NodeKey, ErrGateTypeNotImplemented))
	}
	return e.runScriptGate(ctx, run, snap, node, step)
}

// runScriptGate implements the PRD R5 double-transaction flow:
//
//  1. resolve script source (inline XOR workspace-registered ref)
//  2. txA: INSERT gate_run(status='running') + COMMIT (ms-scale)
//  3. script execution (no tx held; respects gate_timeout_seconds)
//  4. txB: lock run, UPDATE gate_run(result), transitionStepTx, activate
//     downstream or pause, COMMIT (ms-scale)
//  5. post-commit: runSignalAction emits WS events + advances downstream
func (e *Engine) runScriptGate(ctx context.Context, run db.WorkflowRun, snap *Snapshot, node *SnapshotNode, step db.StepInstance) error {
	scriptText, language, maxTimeout, maxOutput, err := e.resolveGateScript(ctx, run.WorkspaceID, node.Config)
	if err != nil {
		return e.failActivation(ctx, run, step, err)
	}

	// Per-node timeout capped by the script's registered max (ref mode)
	// or the global 300s ceiling (inline mode). EffectiveGateTimeoutSeconds
	// already returns 60 for unset, validated to [1,300] at publish.
	timeout := time.Duration(node.Config.EffectiveGateTimeoutSeconds()) * time.Second
	if cap := time.Duration(maxTimeout) * time.Second; timeout > cap {
		timeout = cap
	}

	// ============ txA: INSERT gate_run(running) + COMMIT ============
	tx, err := e.TxStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("gate txA begin: %w", err)
	}
	qtx := e.Queries.WithTx(tx)
	// script_id is recorded when the script came from workflow_gate_script
	// (audit trail for drift detection); inline scripts leave it NULL.
	var scriptID pgtype.UUID
	if node.Config.GateScriptRef != "" {
		// Re-resolve inside txA to capture the ID without holding the
		// txB run lock. A race between txA and a script rename surfaces
		// as a not-found error here, before any gate_run row is written.
		reg, qerr := qtx.GetWorkflowGateScriptByName(ctx, db.GetWorkflowGateScriptByNameParams{
			WorkspaceID: run.WorkspaceID, Name: node.Config.GateScriptRef,
		})
		if qerr != nil {
			if errors.Is(qerr, pgx.ErrNoRows) {
				_ = tx.Rollback(ctx)
				return e.failActivation(ctx, run, step, fmt.Errorf(
					"gate_script_ref %q not found in workspace", node.Config.GateScriptRef))
			}
			_ = tx.Rollback(ctx)
			return fmt.Errorf("gate_script_ref lookup: %w", qerr)
		}
		scriptID = reg.ID
	}
	gateRun, err := qtx.CreateGateRun(ctx, db.CreateGateRunParams{
		StepInstanceID: step.ID,
		ScriptID:       scriptID,
		GateType:       node.Config.GateType,
	})
	if err != nil {
		_ = tx.Rollback(ctx)
		return fmt.Errorf("create gate_run: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("gate txA commit: %w", err)
	}

	// ============ script execution (no tx held) ============
	started := time.Now()
	execOut, runErr := e.executeScript(ctx, scriptText, language, timeout, maxOutput)
	durationMs := int32(time.Since(started).Milliseconds())

	// ============ txB: finalize + advance ============
	tx2, err := e.TxStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("gate txB begin: %w", err)
	}
	defer tx2.Rollback(ctx)
	qtx2 := e.Queries.WithTx(tx2)

	// Lock the run for the duration of the guarded writes (ms-scale).
	lockedRun, err := qtx2.GetWorkflowRunForUpdate(ctx, run.ID)
	if err != nil {
		return fmt.Errorf("gate lock run: %w", err)
	}
	if lockedRun.Status != RunRunning && lockedRun.Status != RunWaitingAcceptance {
		// Run was paused/cancelled/failed while the script ran. The
		// gate_run row stays in 'running' forever (the guarded UPDATE
		// below still finalizes it for audit); the step transition is
		// abandoned so the prevailing status wins.
		_ = finalizeGateRunOnly(ctx, qtx2, gateRun.ID, execOut, runErr, durationMs, node.Config.EffectiveGateOnFail())
		_ = tx2.Commit(ctx)
		return nil
	}

	// Derive gate_run.status + step verdict from script output + error.
	status, gateOutput, verdictResult := deriveGateStatusAndVerdict(execOut, runErr, node.Config.EffectiveGateOnFail())
	outputJSON, _ := json.Marshal(gateOutput)
	if _, err := qtx2.UpdateGateRunResult(ctx, db.UpdateGateRunResultParams{
		ID: gateRun.ID, Status: status, Output: outputJSON, DurationMs: durationMs,
	}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("update gate_run: %w", err)
	}

	// Reload step under the run lock — a duplicate activation / late
	// signal may have already moved it to terminal.
	stepReloaded, err := qtx2.GetStepInstanceForUpdate(ctx, step.ID)
	if err != nil {
		return fmt.Errorf("gate reload step: %w", err)
	}
	if isTerminalStepStatus(stepReloaded.Status) {
		// Already terminal (duplicate signal, retry, rework). Commit
		// the gate_run finalization; do not transition the step.
		_ = tx2.Commit(ctx)
		return nil
	}

	triggerBy := "system"
	payload := map[string]any{
		"gate_run_id": util.UUIDToString(gateRun.ID),
		"gate_status": status,
		"fix_hint":    gateOutput.FixHint,
		"facts":       gateOutput.Facts,
	}
	if gateOutput.Dispositions != nil {
		payload["dispositions"] = gateOutput.Dispositions
	}

	action := signalAction{kind: "none", run: lockedRun, snap: snap, prevStep: stepReloaded}
	switch verdictResult {
	case StepPassed:
		// Mirror consumeVerdictTx's VerdictPass routing: activate the
		// downstream step if any; complete the run at the chain tail;
		// pause + block if edges exist but none matched (defensive —
		// gate edges are catch-all per R7 so this branch is unreachable
		// for valid templates, but stays fail-visible for hand-edited
		// snapshots).
		next := snap.NextAfterAll(stepReloaded.NodeKey, buildEvalCtx(nil, nil, ParseRunContext(lockedRun.Context)))
		if len(next) > 0 {
			if !e.transitionStepTx(ctx, qtx2, stepReloaded, StepPassed, triggerBy, payload) {
				return nil // lost guard race; another consumer advanced
			}
			for _, nxt := range next {
				nextStep, err := activateStepTx(ctx, qtx2, lockedRun.ID, nxt.NodeKey)
				if err != nil {
					return fmt.Errorf("gate activate downstream %q: %w", nxt.NodeKey, err)
				}
				for _, after := range lookaheadTargets(snap, &nxt) {
					if err := preCreateStepTx(ctx, qtx2, lockedRun.ID, after.NodeKey); err != nil {
						return fmt.Errorf("gate pre-create lookahead %q: %w", after.NodeKey, err)
					}
				}
				n := nxt // loop var capture safety
				action.nextNodes = append(action.nextNodes, &n)
				action.nextSteps = append(action.nextSteps, nextStep)
			}
			action.kind = "advance"
		} else if snap.OutEdgeCount(stepReloaded.NodeKey) > 0 {
			if !e.transitionStepTx(ctx, qtx2, stepReloaded, StepBlocked, triggerBy, payload) {
				return nil
			}
			if !e.pauseRunTx(ctx, qtx2, lockedRun) {
				return nil
			}
			action.kind = "blocked"
		} else {
			if !e.transitionStepTx(ctx, qtx2, stepReloaded, StepPassed, triggerBy, payload) {
				return nil
			}
			action.kind = "complete"
		}
	case StepBlocked:
		if !e.transitionStepTx(ctx, qtx2, stepReloaded, StepBlocked, triggerBy, payload) {
			return nil
		}
		if !e.pauseRunTx(ctx, qtx2, lockedRun) {
			return nil
		}
		action.kind = "blocked"
	default:
		// verdictResult is one of StepPassed/StepBlocked only; any other
		// value is a programming error. Surface it rather than silently
		// dropping the step.
		return fmt.Errorf("workflow: gate %q derived unknown verdict %q", node.NodeKey, verdictResult)
	}

	if err := tx2.Commit(ctx); err != nil {
		return fmt.Errorf("gate txB commit: %w", err)
	}

	// Post-commit: emit WS events, advance downstream / pause / notify.
	// runSignalAction's blocked arm calls notifyInitiator for us.
	return e.runSignalAction(ctx, action)
}

// finalizeGateRunOnly is the no-step-transition fallback used when the
// run has left the active state while the script was executing. The
// gate_run row is finalized for audit even though the step stays put.
// Returns the finalize error (caller decides whether to surface).
func finalizeGateRunOnly(ctx context.Context, qtx *db.Queries, gateRunID pgtype.UUID, res gateExecResult, runErr error, durationMs int32, onFail string) error {
	status, gateOutput, _ := deriveGateStatusAndVerdict(res, runErr, onFail)
	outputJSON, _ := json.Marshal(gateOutput)
	_, err := qtx.UpdateGateRunResult(ctx, db.UpdateGateRunResultParams{
		ID: gateRunID, Status: status, Output: outputJSON, DurationMs: durationMs,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // already finalized by a recovery path
	}
	return err
}

// resolveGateScript returns the script body + runtime parameters. Inline
// scripts use the node config's language/timeout defaults; registered
// scripts inherit the workspace_gate_script row's language + per-script
// caps (the per-node timeout is still clamped against the script's
// max_timeout_seconds in runScriptGate).
func (e *Engine) resolveGateScript(ctx context.Context, workspaceID pgtype.UUID, cfg NodeConfig) (script string, language string, maxTimeout int32, maxOutput int32, err error) {
	language = cfg.EffectiveGateLanguage()
	maxTimeout = gateTimeoutMaxSeconds // global ceiling for inline scripts
	maxOutput = gateDefaultMaxOutputBytes
	if cfg.GateInlineScript != "" {
		script = cfg.GateInlineScript
		return
	}
	if cfg.GateScriptRef != "" {
		reg, qerr := e.Queries.GetWorkflowGateScriptByName(ctx, db.GetWorkflowGateScriptByNameParams{
			WorkspaceID: workspaceID, Name: cfg.GateScriptRef,
		})
		if qerr != nil {
			if errors.Is(qerr, pgx.ErrNoRows) {
				err = fmt.Errorf("gate_script_ref %q not found in workspace", cfg.GateScriptRef)
				return
			}
			err = fmt.Errorf("gate_script_ref %q lookup: %w", cfg.GateScriptRef, qerr)
			return
		}
		script = reg.ScriptText
		language = reg.Language
		maxTimeout = reg.MaxTimeoutSeconds
		maxOutput = reg.MaxOutputBytes
		return
	}
	// validateGateConfig rejects this at publish time; the runtime guard
	// is defensive for hand-edited snapshots.
	err = errors.New("workflow: gate node requires gate_inline_script or gate_script_ref")
	return
}

// executeScript runs the script body under sh -c (shell) or python3 -c
// (python3), enforcing the timeout and output-byte ceiling. Returns the
// captured stdout/stderr + a non-nil error when the script timed out,
// exited non-zero with no parseable output, or failed to start.
//
// The captured output is ALWAYS returned alongside the error so the
// caller can record partial output in gate_run.output for audit even
// when the run failed.
func (e *Engine) executeScript(ctx context.Context, script string, language string, timeout time.Duration, maxOutput int32) (gateExecResult, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var cmd *exec.Cmd
	switch language {
	case GateLanguagePython3:
		cmd = exec.CommandContext(ctx, "python3", "-c", script)
	default:
		cmd = exec.CommandContext(ctx, "sh", "-c", script)
	}
	cmd.Env = allowlistEnv()
	// cmd.Dir intentionally unset (inherits server cwd). Workdir
	// association (step.issue_id workdir) lands in v2 alongside the
	// daemon-execution refactor.

	var stdoutBuf, stderrBuf bytes.Buffer
	// Cap is int32 in the DB schema (max 10MB); int range on every
	// plausible host, but cast at the boundary so the limit writer
	// (which uses plain int) cannot overflow on huge configured values.
	cap := int(maxOutput)
	if cap < 0 {
		cap = 0
	}
	cmd.Stdout = newLimitWriter(&stdoutBuf, cap)
	cmd.Stderr = newLimitWriter(&stderrBuf, cap)

	runErr := cmd.Run()

	var res gateExecResult
	res.stdout = stdoutBuf.String()
	res.stderr = stderrBuf.String()
	// Truncation flag: the limit writer silently drops bytes past the
	// cap, so a full buffer signals truncation whether or not the run
	// errored.
	if len(res.stdout) >= cap || len(res.stderr) >= cap {
		res.truncated = true
	}

	if ctx.Err() == context.DeadlineExceeded {
		res.stderr = fmt.Sprintf("timeout after %s", timeout)
		return res, errors.New("workflow: gate script timeout")
	}
	if runErr != nil && len(res.stdout) == 0 {
		// Exit code non-zero AND no stdout — treat as a hard error so
		// the gate derives status=error → VerdictBlocked. Any stdout
		// (even with non-zero exit) goes through parseGateOutput so the
		// script can signal `block` deliberately via exit-1 + JSON.
		return res, fmt.Errorf("workflow: gate script failed: %w", runErr)
	}
	return res, nil
}

// limitWriter wraps a *bytes.Buffer and silently discards bytes past the
// cap. It always reports the full write length to the caller so the
// underlying command does not fail with SIGPIPE/EPIPE (which would mask
// the real exit code and lose stderr). The Truncated flag is derived
// from the buffer length by the caller.
type limitWriter struct {
	buf       *bytes.Buffer
	remaining int
}

func (lw *limitWriter) Write(p []byte) (int, error) {
	if lw.remaining <= 0 {
		return len(p), nil
	}
	n := len(p)
	if n > lw.remaining {
		n = lw.remaining
	}
	lw.buf.Write(p[:n])
	lw.remaining -= n
	return len(p), nil
}

func newLimitWriter(buf *bytes.Buffer, limit int) *limitWriter {
	return &limitWriter{buf: buf, remaining: limit}
}

// allowlistEnv returns only PATH/HOME/LANG/LC_ALL/TZ from the server's
// environment. DATABASE_URL / *_SECRET / *_TOKEN / MULTICA_* never reach
// the gate script (PRD R6 contract #1 + review Issue 2.A). A missing
// variable (LANG not set, etc.) is silently omitted; the script gets
// only what was already present.
func allowlistEnv() []string {
	allow := map[string]bool{
		"PATH":  true,
		"HOME":  true,
		"LANG":  true,
		"LC_ALL": true,
		"TZ":    true,
	}
	out := make([]string, 0, len(allow))
	for _, kv := range os.Environ() {
		key := kv
		if i := strings.IndexByte(kv, '='); i > 0 {
			key = kv[:i]
		}
		if allow[key] {
			out = append(out, kv)
		}
	}
	return out
}

// parseGateOutput parses the last non-empty line of stdout as the gate
// script's structured verdict (design.md §2.3). Returns ok=false when:
//   - stdout is empty
//   - the last non-empty line is not valid JSON
//   - the JSON parses but the status field is outside {pass,block,warn}
//
// Non-JSON output is a script bug; the caller maps it to status=error →
// VerdictBlocked so the run pauses rather than silently advancing.
func parseGateOutput(stdout string) (GateOutput, bool) {
	trimmed := strings.TrimRight(stdout, "\n")
	if trimmed == "" {
		return GateOutput{}, false
	}
	lines := strings.Split(trimmed, "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	if last == "" {
		return GateOutput{}, false
	}
	var parsed GateOutput
	if err := json.Unmarshal([]byte(last), &parsed); err != nil {
		return GateOutput{}, false
	}
	switch parsed.Status {
	case "pass", "block", "warn":
		return parsed, true
	default:
		return GateOutput{}, false
	}
}

// deriveGateStatusAndVerdict maps (script output, script error, on_fail)
// to (gate_run.status, GateRunOutput for JSONB, step verdict). The
// verdict matrix (PRD Q5 + R5 tail):
//
//	error       (timeout / exit-nonzero-no-JSON / non-JSON last line)
//	            → status=error,    VerdictBlocked
//	pass        → status=pass,     VerdictPassed (= StepPassed)
//	warn        → status=warn,     VerdictPassed (warn never blocks)
//	block + on_fail=block → status=block, VerdictBlocked
//	block + on_fail=warn  → status=block, VerdictPassed (record + advance)
//
// fix_hint is sanitized via the P0 helper (sanitizePromptText) so control
// characters and terminal escapes cannot smuggle into the verdict payload
// or the downstream agent's prompt (PRD R6 contract #5).
func deriveGateStatusAndVerdict(res gateExecResult, runErr error, onFail string) (string, GateRunOutput, string) {
	var gro GateRunOutput
	gro.Stdout = res.stdout
	gro.Stderr = res.stderr
	gro.Truncated = res.truncated

	if runErr != nil {
		// Timeout, exit-nonzero-no-stdout, or startup failure. Stderr
		// already carries the cause (timeout message or exec error).
		return "error", gro, StepBlocked
	}

	parsed, ok := parseGateOutput(res.stdout)
	if !ok {
		// Stdout's last line was non-JSON or had no status — script
		// bug. Status=error so the audit trail shows the failure; the
		// step pauses the run.
		return "error", gro, StepBlocked
	}

	if parsed.FixHint != "" {
		gro.FixHint = sanitizePromptText(parsed.FixHint)
	}
	gro.Facts = parsed.Facts
	gro.Dispositions = parsed.Dispositions

	switch parsed.Status {
	case "pass":
		return "pass", gro, StepPassed
	case "warn":
		// warn is advisory: record + advance.
		return "warn", gro, StepPassed
	case "block":
		if onFail == GateOnFailWarn {
			// block + on_fail=warn → pass with evidence (PRD Q5).
			return "block", gro, StepPassed
		}
		return "block", gro, StepBlocked
	}
	// parseGateOutput already rejected non-enum status; this is unreachable.
	return "error", gro, StepBlocked
}
