package handler

// workflow_gate_e2e_test.go — P1-3 Wave 3 acceptance-criteria suite
// (implement.md step 3.1). One test per AC over the HTTP handler surface
// for the new structural gate node type (script form MVP; PRD R5 + R6).
//
// Wave 2 already pins the gate-node semantics at the engine package layer
// (internal/workflow/template_gate_test.go for publish validation,
// internal/workflow/gate.go unit tests for parseGateOutput /
// deriveGateStatusAndVerdict / allowlistEnv). Wave 3's value-add is the
// HTTP e2e proof: the wiring from POST /submission all the way to gate
// activation → script exec → gate_run row + step verdict + run state +
// inbox notification, plus the publish-time and flag-off surfaces the
// engine unit tests cannot reach.
//
// Canonical shape under test (gateE2ETemplate):
//
//	work(executor) → gate(gate_type=script, inline) → end
//
// When the upstream executor submission derives the system pass verdict,
// consumeVerdictTx activates the gate step, which runs the script
// synchronously inside activateGateNode and transitions itself to
// passed/blocked. The PRD Q5 verdict matrix maps (gate status,
// gate_on_fail) → step verdict:
//
//	pass                 → StepPassed  (run advances)
//	warn                 → StepPassed  (advisory, never blocks)
//	block + on_fail=block → StepBlocked (run paused + inbox)
//	block + on_fail=warn  → StepPassed  (evidence retained, run advances)
//	error                → StepBlocked (run paused + inbox)
//
// fix_hint is sanitized via the P0 sanitizePromptText path so control
// characters and terminal escapes cannot leak into the verdict payload
// (PRD R6 contract #5).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/workflow"
	"github.com/multica-ai/multica/server/pkg/featureflag"
)

// ---------------------------------------------------------------------------
// Template helpers
// ---------------------------------------------------------------------------

// gateE2ETemplate builds the canonical P1-3 gate shape with a
// parameterizable gate config:
//
//	work(executor) → gate(gate_type=script) → end
//
// The caller fills the gate NodeConfig (gate_type + script source +
// gate_on_fail + gate_timeout_seconds + gate_language). The work node
// declares no required exit fields so DONE with an empty exit_fields map
// is schema-valid (the gate script is what decides pass/block).
func gateE2ETemplate(t *testing.T, gateCfg workflow.NodeConfig) ([]workflow.NodeInput, []workflow.EdgeInput) {
	t.Helper()
	work := workflow.NodeInput{
		NodeKey: "work", Type: workflow.NodeTypeAgent, Name: "work",
		Config: agentCfg(t, workflow.NodeConfig{
			Role:          workflow.RoleExecutor,
			AgentSelector: "WF Placeholder Executor",
			Instructions:  "Implement the requirement",
		}),
	}
	gate := workflow.NodeInput{
		NodeKey: "gate", Type: workflow.NodeTypeGate, Name: "gate",
		Config: agentCfg(t, gateCfg),
	}
	end := workflow.NodeInput{
		NodeKey: "end", Type: workflow.NodeTypeEnd, Name: "end",
		Config: agentCfg(t, workflow.NodeConfig{}),
	}
	return []workflow.NodeInput{work, gate, end}, []workflow.EdgeInput{
		{FromNodeKey: "work", ToNodeKey: "gate"},
		{FromNodeKey: "gate", ToNodeKey: "end"},
	}
}

// gateE2ETemplateWithUpstreamField is gateE2ETemplate with the upstream
// work node declaring a required exit field. Used by tests that need the
// upstream submission to carry a payload (negative schema-validation
// cases live in workflow_fanout_e2e_test.go, not here).
func gateE2ETemplateWithUpstreamField(t *testing.T, gateCfg workflow.NodeConfig, fieldName, fieldType string) ([]workflow.NodeInput, []workflow.EdgeInput) {
	t.Helper()
	nodes, edges := gateE2ETemplate(t, gateCfg)
	for i := range nodes {
		if nodes[i].NodeKey != "work" {
			continue
		}
		cfg, err := workflow.ParseNodeConfig(nodes[i].Config)
		if err != nil {
			t.Fatalf("parse work cfg: %v", err)
		}
		cfg.ExitFields = &workflow.ExitFieldsSchema{Fields: []workflow.ExitFieldSpec{
			{Name: fieldName, Type: fieldType, Required: true},
		}}
		raw, _ := json.Marshal(cfg)
		nodes[i].Config = raw
	}
	return nodes, edges
}

// postWorkDone submits the work step's DONE with an empty exit_fields
// map (the canonical gateE2ETemplate upstream declares no required
// fields). Returns the HTTP recorder for status inspection.
func postWorkDone(t *testing.T, wh *WorkflowHandler, taskID, agentID string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	wh.CreateSubmission(w, matRequest(t, "POST", "/api/tasks/"+taskID+"/submission", taskID, agentID, map[string]any{
		"status":      workflow.SubmissionDone,
		"exit_fields": map[string]any{},
	}))
	return w
}

// gateRunStatusEventually polls the gate_run status until it leaves "running"
// (the txB UPDATE that finalizes the gate_run lands a beat after the
// post-submission read on slow CI runners) or a 10s deadline passes. Returns
// the terminal status. Stabilizes TestAC7/AC8 against the CI status-update
// race that the process-group kill (P1-4 gate fix) reduced but did not fully
// eliminate on GitHub Actions runners.
func gateRunStatusEventually(t *testing.T, runID, nodeKey string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		status, _ := gateRunForRunStep(t, runID, nodeKey)["status"].(string)
		if status != "running" {
			return status
		}
		if time.Now().After(deadline) {
			return status
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// gateRunForRunStep returns the latest gate_run row for the given run's
// gate step. The gate node always activates exactly one gate_run per
// activation; querying by step_instance_id keeps the assertion local.
func gateRunForRunStep(t *testing.T, runID, nodeKey string) map[string]any {
	t.Helper()
	var output []byte
	var status, gateType string
	var scriptID *string
	if err := testPool.QueryRow(context.Background(), `
		SELECT gr.status, gr.gate_type, gr.output, gr.script_id::text
		FROM gate_run gr
		JOIN step_instance si ON si.id = gr.step_instance_id
		WHERE si.run_id = $1 AND si.node_key = $2
		ORDER BY gr.created_at DESC LIMIT 1
	`, runID, nodeKey).Scan(&status, &gateType, &output, &scriptID); err != nil {
		t.Fatalf("read gate_run for run=%s node=%s: %v", runID, nodeKey, err)
	}
	out := map[string]any{
		"status":    status,
		"gate_type": gateType,
		"raw":       string(output),
	}
	if len(output) > 0 {
		var parsed map[string]any
		if jerr := json.Unmarshal(output, &parsed); jerr == nil {
			out["output"] = parsed
		}
	}
	if scriptID != nil {
		out["script_id"] = *scriptID
	}
	return out
}

// ---------------------------------------------------------------------------
// AC2: publish-time validation — gate config invariants
// ---------------------------------------------------------------------------

// TestAC2GatePublishValidation covers the six publish-time refusals from
// PRD AC2 / R4: missing gate_type, both ref+inline set, inline > 4KB,
// invalid gate_on_fail, invalid gate_language, out-of-range timeout.
// Each case must fail at PublishTemplate (422-equivalent) before any run
// starts. The validateGateConfig error strings are pinned by Wave 2's
// internal/workflow/template_gate_test.go; this test pins the SAME rules
// surfacing through the public TemplateService API.
func TestAC2GatePublishValidation(t *testing.T) {
	ctx := context.Background()

	publish := func(gateCfg workflow.NodeConfig) error {
		nodes, edges := gateE2ETemplate(t, gateCfg)
		templates := workflow.NewTemplateService(testHandler.Queries, testPool)
		detail, err := templates.CreateTemplate(ctx, workflow.CreateTemplateParams{
			WorkspaceID: util.MustParseUUID(testWorkspaceID),
			Key:         fmt.Sprintf("ac2-publish-%d", time.Now().UnixNano()),
			Name:        "ac2",
			CreatedBy:   util.MustParseUUID(testUserID),
			Nodes:       nodes, Edges: edges,
		})
		if err != nil {
			return err
		}
		_, perr := templates.PublishTemplate(ctx, util.MustParseUUID(testWorkspaceID), detail.Template.ID)
		return perr
	}

	t.Run("missing_gate_type", func(t *testing.T) {
		err := publish(workflow.NodeConfig{
			GateInlineScript: `echo hi`,
		})
		if err == nil {
			t.Fatalf("publish without gate_type: expected error, got nil")
		}
		if !strings.Contains(err.Error(), "gate_type") {
			t.Fatalf("publish err = %v, want mention of gate_type", err)
		}
	})

	t.Run("both_ref_and_inline", func(t *testing.T) {
		err := publish(workflow.NodeConfig{
			GateType:         workflow.GateTypeScript,
			GateScriptRef:    "registered-script",
			GateInlineScript: `echo hi`,
		})
		if err == nil {
			t.Fatalf("publish with both ref + inline: expected error, got nil")
		}
		if !strings.Contains(err.Error(), "gate_script_ref") || !strings.Contains(err.Error(), "gate_inline_script") {
			t.Fatalf("publish err = %v, want mention of both gate_script_ref and gate_inline_script", err)
		}
	})

	t.Run("inline_too_large", func(t *testing.T) {
		tooBig := strings.Repeat("x", 4097) // gateInlineScriptMaxBytes + 1
		err := publish(workflow.NodeConfig{
			GateType:         workflow.GateTypeScript,
			GateInlineScript: tooBig,
		})
		if err == nil {
			t.Fatalf("publish with >4KB inline: expected error, got nil")
		}
		if !strings.Contains(err.Error(), "gate_inline_script") || !strings.Contains(err.Error(), "max") {
			t.Fatalf("publish err = %v, want mention of gate_inline_script + max", err)
		}
	})

	t.Run("invalid_on_fail", func(t *testing.T) {
		// ParseNodeConfig rejects the bogus enum before validateGateConfig
		// even runs, so the failure surfaces at template creation. Both
		// code paths refuse the value; the assertion is "publish fails
		// mentioning gate_on_fail".
		err := publish(workflow.NodeConfig{
			GateType:         workflow.GateTypeScript,
			GateInlineScript: `echo hi`,
			GateOnFail:       "explode",
		})
		if err == nil {
			t.Fatalf("publish with gate_on_fail=explode: expected error, got nil")
		}
		if !strings.Contains(err.Error(), "gate_on_fail") {
			t.Fatalf("publish err = %v, want mention of gate_on_fail", err)
		}
	})

	t.Run("invalid_language", func(t *testing.T) {
		err := publish(workflow.NodeConfig{
			GateType:         workflow.GateTypeScript,
			GateInlineScript: `echo hi`,
			GateLanguage:     "ruby",
		})
		if err == nil {
			t.Fatalf("publish with gate_language=ruby: expected error, got nil")
		}
		if !strings.Contains(err.Error(), "gate_language") {
			t.Fatalf("publish err = %v, want mention of gate_language", err)
		}
	})

	t.Run("invalid_timeout", func(t *testing.T) {
		err := publish(workflow.NodeConfig{
			GateType:           workflow.GateTypeScript,
			GateInlineScript:   `echo hi`,
			GateTimeoutSeconds: 301,
		})
		if err == nil {
			t.Fatalf("publish with gate_timeout_seconds=301: expected error, got nil")
		}
		if !strings.Contains(err.Error(), "gate_timeout_seconds") {
			t.Fatalf("publish err = %v, want mention of gate_timeout_seconds", err)
		}
	})
}

// ---------------------------------------------------------------------------
// AC3: gate_type != script → publish OK, activate fails
// ---------------------------------------------------------------------------

// TestAC3GateTypeNotImplemented pins the P1-3c/P1-4 forward-compat
// contract: rules and hybrid gate types PASS publish validation (PRD
// §Resolved Decisions Q1 + Wave 2 forward-compat test), but the
// runtime refuses to dispatch them via ErrGateTypeNotImplemented
// (PRD R1 / AC3). The observable HTTP-surface behavior is the same as
// any failActivation: gate step → blocked, run → paused, initiator
// notified.
//
// P1-3b: agent and adversarial are now ACTIVATED (they were on this
// list in P1-3 MVP). Their end-to-end behavior is covered by the
// P1-3b suite (workflow_gate_agent_e2e_test.go).
//
// publish passes for rules/hybrid so a template carrying them does not
// require a follow-up migration when P1-3c/P1-4 activates them; the
// runtime refusal is fail-visible rather than silent.
func TestAC3GateTypeNotImplemented(t *testing.T) {
	for _, gt := range []string{workflow.GateTypeRules, workflow.GateTypeHybrid} {
		t.Run(gt, func(t *testing.T) {
			gateCfg := workflow.NodeConfig{GateType: gt}
			nodes, edges := gateE2ETemplate(t, gateCfg)
			f := setupWorkflowAPIFixture(t, "ac3-"+gt, nodes, edges)
			runID := uuidToString(f.run.ID)

			// Publish accepted the template (setupWorkflowAPIFixture
			// would have Fatal'd otherwise). Now drive the upstream
			// work step to DONE — gate activates → ErrGateTypeNotImplemented.
			workTask := f.stepTask(t, "work")
			w := postWorkDone(t, f.wh, workTask, f.executorID)
			if w.Code != http.StatusCreated {
				t.Fatalf("upstream submission = %d, want 201; body=%s", w.Code, w.Body.String())
			}

			// gate step → blocked (failActivation).
			if got := stepStatusForNodeKey(t, runID, "gate"); got != workflow.StepBlocked {
				t.Fatalf("gate step status = %q, want blocked (ErrGateTypeNotImplemented)", got)
			}
			// run → paused or failed (failActivation parks the run).
			if got := runStatusForRun(t, runID); got != workflow.RunPaused && got != workflow.RunFailed {
				t.Fatalf("run status = %q, want paused or failed", got)
			}
			// Initiator notified.
			if n := countInboxType(t, "workflow_blocked"); n < 1 {
				t.Fatalf("workflow_blocked inbox = %d, want >=1 (initiator notified)", n)
			}
			// No gate_run row should be written — the type dispatch
			// refuses BEFORE the txA INSERT (gate.go activateGateNode
			// dispatches to ErrGateTypeNotImplemented before
			// activateGateAgentNode or runScriptGate runs).
			var gateRunCount int
			if err := testPool.QueryRow(context.Background(), `
				SELECT count(*) FROM gate_run gr
				JOIN step_instance si ON si.id = gr.step_instance_id
				WHERE si.run_id = $1
			`, runID).Scan(&gateRunCount); err != nil {
				t.Fatalf("count gate_run: %v", err)
			}
			if gateRunCount != 0 {
				t.Fatalf("gate_run rows = %d, want 0 (type dispatch refuses before txA)", gateRunCount)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// AC4: inline shell script → pass
// ---------------------------------------------------------------------------

// TestAC4ScriptGatePass pins the happy path (PRD AC4): an inline shell
// script emitting `{"status":"pass"}` drives the gate step to passed,
// the gate_run row to status=pass, and the run to completed (the end
// tail activates once the gate verdict is StepPassed).
func TestAC4ScriptGatePass(t *testing.T) {
	gateCfg := workflow.NodeConfig{
		GateType:         workflow.GateTypeScript,
		GateInlineScript: `echo '{"status":"pass"}'`,
	}
	nodes, edges := gateE2ETemplate(t, gateCfg)
	f := setupWorkflowAPIFixture(t, "ac4-pass", nodes, edges)
	runID := uuidToString(f.run.ID)

	workTask := f.stepTask(t, "work")
	w := postWorkDone(t, f.wh, workTask, f.executorID)
	if w.Code != http.StatusCreated {
		t.Fatalf("upstream submission = %d, want 201; body=%s", w.Code, w.Body.String())
	}

	// gate step → passed.
	if got := stepStatusForNodeKey(t, runID, "gate"); got != workflow.StepPassed {
		t.Fatalf("gate step = %q, want passed", got)
	}
	// gate_run row → status=pass.
	gr := gateRunForRunStep(t, runID, "gate")
	if got := gr["status"]; got != "pass" {
		t.Fatalf("gate_run.status = %v, want pass", got)
	}
	if got := gr["gate_type"]; got != workflow.GateTypeScript {
		t.Fatalf("gate_run.gate_type = %v, want %q", got, workflow.GateTypeScript)
	}
	// script_id is NULL for inline scripts (audit trail distinction).
	if _, ok := gr["script_id"]; ok {
		t.Fatalf("gate_run.script_id = %v, want absent for inline scripts", gr["script_id"])
	}
	// end step → activated and the run completes.
	if got := stepStatusForNodeKey(t, runID, "end"); got != workflow.StepPassed && got != workflow.StepActive {
		t.Fatalf("end step = %q, want passed or active (post-gate activation)", got)
	}
	if got := runStatusForRun(t, runID); got != workflow.RunCompleted {
		t.Fatalf("run = %q, want completed (gate pass advances the chain)", got)
	}
}

// ---------------------------------------------------------------------------
// AC5: inline shell script → block + on_fail=block
// ---------------------------------------------------------------------------

// TestAC5ScriptGateBlock pins the block-with-default-on_fail path (PRD
// AC5): an inline shell script emitting
// `{"status":"block","fix_hint":"..."}` with gate_on_fail=block (the
// default) drives the gate step to blocked, the run to paused, and
// notifies the reviewer via inbox. fix_hint is preserved (sanitization
// is exercised in AC9).
func TestAC5ScriptGateBlock(t *testing.T) {
	gateCfg := workflow.NodeConfig{
		GateType:         workflow.GateTypeScript,
		GateInlineScript: `echo '{"status":"block","fix_hint":"missing tests for foo()"}'`,
		// gate_on_fail unset → EffectiveGateOnFail() returns block.
	}
	nodes, edges := gateE2ETemplate(t, gateCfg)
	f := setupWorkflowAPIFixture(t, "ac5-block", nodes, edges)
	runID := uuidToString(f.run.ID)

	workTask := f.stepTask(t, "work")
	w := postWorkDone(t, f.wh, workTask, f.executorID)
	if w.Code != http.StatusCreated {
		t.Fatalf("upstream submission = %d, want 201; body=%s", w.Code, w.Body.String())
	}

	if got := stepStatusForNodeKey(t, runID, "gate"); got != workflow.StepBlocked {
		t.Fatalf("gate step = %q, want blocked", got)
	}
	if got := runStatusForRun(t, runID); got != workflow.RunPaused && got != workflow.RunFailed {
		t.Fatalf("run = %q, want paused or failed (gate blocked)", got)
	}
	gr := gateRunForRunStep(t, runID, "gate")
	if got := gr["status"]; got != "block" {
		t.Fatalf("gate_run.status = %v, want block", got)
	}
	// fix_hint survives into gate_run.output.
	out, _ := gr["output"].(map[string]any)
	if out == nil || out["fix_hint"] != "missing tests for foo()" {
		t.Fatalf("gate_run.output.fix_hint = %v, want %q", out["fix_hint"], "missing tests for foo()")
	}
	// Reviewer/initiator notified.
	if n := countInboxType(t, "workflow_blocked"); n < 1 {
		t.Fatalf("workflow_blocked inbox = %d, want >=1 (reviewer notified)", n)
	}
	// end step stays non-passed (gate blocks the chain).
	if got := stepStatusForNodeKey(t, runID, "end"); got == workflow.StepPassed {
		t.Fatalf("end = passed; want any non-passed (gate blocked the chain)")
	}
}

// ---------------------------------------------------------------------------
// AC6: gate_on_fail=warn → block becomes pass with evidence
// ---------------------------------------------------------------------------

// TestAC6ScriptGateBlockOnFailWarn pins the on_fail=warn advisory path
// (PRD AC6 / Q5): a block verdict with gate_on_fail=warn does NOT block
// the chain — the step transitions to passed and the run advances to
// completion, with the gate's output.fix_hint retained as evidence in
// the verdict payload.
func TestAC6ScriptGateBlockOnFailWarn(t *testing.T) {
	gateCfg := workflow.NodeConfig{
		GateType:         workflow.GateTypeScript,
		GateInlineScript: `echo '{"status":"block","fix_hint":"noted but non-fatal"}'`,
		GateOnFail:       workflow.GateOnFailWarn,
	}
	nodes, edges := gateE2ETemplate(t, gateCfg)
	f := setupWorkflowAPIFixture(t, "ac6-warn", nodes, edges)
	runID := uuidToString(f.run.ID)

	workTask := f.stepTask(t, "work")
	w := postWorkDone(t, f.wh, workTask, f.executorID)
	if w.Code != http.StatusCreated {
		t.Fatalf("upstream submission = %d, want 201; body=%s", w.Code, w.Body.String())
	}

	// gate step → passed (on_fail=warn promotes block to pass).
	if got := stepStatusForNodeKey(t, runID, "gate"); got != workflow.StepPassed {
		t.Fatalf("gate step = %q, want passed (on_fail=warn promotes block)", got)
	}
	// gate_run.status stays block — the script's verdict is preserved
	// verbatim; only the derived step verdict changes (PRD Q5).
	gr := gateRunForRunStep(t, runID, "gate")
	if got := gr["status"]; got != "block" {
		t.Fatalf("gate_run.status = %v, want block (script verdict preserved)", got)
	}
	// Evidence retained in gate_run.output.fix_hint.
	out, _ := gr["output"].(map[string]any)
	if out == nil || out["fix_hint"] != "noted but non-fatal" {
		t.Fatalf("gate_run.output.fix_hint = %v, want evidence retained", out["fix_hint"])
	}
	// Run advances through end → completed.
	if got := runStatusForRun(t, runID); got != workflow.RunCompleted {
		t.Fatalf("run = %q, want completed (block+warn does not pause)", got)
	}
	// fix_hint lands in the verdict payload (transitionStepTx writes
	// the gate output into the step transition payload).
	var payloadFixHint any
	if err := testPool.QueryRow(context.Background(), `
		SELECT st.payload->>'fix_hint'
		FROM step_transition st
		JOIN step_instance si ON si.id = st.step_instance_id
		WHERE si.run_id = $1 AND si.node_key = 'gate'
		  AND st.to_status = 'passed'
		ORDER BY st.occurred_at DESC LIMIT 1
	`, runID).Scan(&payloadFixHint); err == nil {
		if payloadFixHint != "noted but non-fatal" {
			t.Fatalf("verdict payload fix_hint = %v, want %q", payloadFixHint, "noted but non-fatal")
		}
	}
}

// ---------------------------------------------------------------------------
// AC7: inline shell script → timeout → error → blocked
// ---------------------------------------------------------------------------

// TestAC7ScriptGateTimeout pins the timeout path (PRD AC7): an inline
// script that exceeds gate_timeout_seconds surfaces as gate_run.status=
// error, step → blocked, run → paused. The 3s timeout + 6s sleep keeps
// the test fast while exercising context.Cancel + the stderr "timeout
// after Xs" override in executeScript. The 3s margin (was 1s) absorbs CI
// runner jitter: at 1s the gate_run had often not flipped to 'error' before
// the post-submit assertion read it, making the test flaky on slow CI
// machines (3 consecutive failures on PR #10).
func TestAC7ScriptGateTimeout(t *testing.T) {
	gateCfg := workflow.NodeConfig{
		GateType:           workflow.GateTypeScript,
		GateInlineScript:   `sleep 6`,
		GateTimeoutSeconds: 3,
	}
	nodes, edges := gateE2ETemplate(t, gateCfg)
	f := setupWorkflowAPIFixture(t, "ac7-timeout", nodes, edges)
	runID := uuidToString(f.run.ID)

	workTask := f.stepTask(t, "work")
	w := postWorkDone(t, f.wh, workTask, f.executorID)
	if w.Code != http.StatusCreated {
		t.Fatalf("upstream submission = %d, want 201; body=%s", w.Code, w.Body.String())
	}

	if got := stepStatusForNodeKey(t, runID, "gate"); got != workflow.StepBlocked {
		t.Fatalf("gate step = %q, want blocked (timeout → error → block)", got)
	}
	if got := runStatusForRun(t, runID); got != workflow.RunPaused && got != workflow.RunFailed {
		t.Fatalf("run = %q, want paused or failed (timeout)", got)
	}
	if got := gateRunStatusEventually(t, runID, "gate"); got != "error" {
		t.Fatalf("gate_run.status = %v, want error (timeout)", got)
	}
	gr := gateRunForRunStep(t, runID, "gate")
	// stderr carries the timeout message (executeScript overrides
	// stderr with "timeout after Xs" on context.DeadlineExceeded).
	out, _ := gr["output"].(map[string]any)
	if out == nil {
		t.Fatalf("gate_run.output missing; raw=%s", gr["raw"])
	}
	if got, _ := out["stderr"].(string); !strings.Contains(got, "timeout") {
		t.Fatalf("gate_run.output.stderr = %q, want contains 'timeout'", got)
	}
	// Reviewer/initiator notified.
	if n := countInboxType(t, "workflow_blocked"); n < 1 {
		t.Fatalf("workflow_blocked inbox = %d, want >=1 (reviewer notified on timeout)", n)
	}
}

// ---------------------------------------------------------------------------
// AC8: inline shell script → output truncation → error → blocked
// ---------------------------------------------------------------------------

// TestAC8ScriptGateOutputTruncation pins the output-cap contract (PRD
// AC8 / R6 contract #2): an inline script emitting >1MB to stdout has
// its output truncated to the default cap (1MB), the truncation flag
// flips to true, and the resulting non-JSON last line drives the gate
// to status=error → step blocked.
//
// `yes | head -c 2097152` emits exactly 2 MiB of 'y\n' bytes — well
// above the 1 MiB default cap (gateDefaultMaxOutputBytes). The limit
// writer silently drops the overflow, so the in-buffer stdout never
// contains a JSON last line, parseGateOutput returns ok=false, and the
// derived gate_run.status is "error" with output.truncated=true.
func TestAC8ScriptGateOutputTruncation(t *testing.T) {
	gateCfg := workflow.NodeConfig{
		GateType:         workflow.GateTypeScript,
		GateInlineScript: `yes | head -c 2097152`,
		// gate_timeout_seconds default 60s is plenty for 2MB of yes.
	}
	nodes, edges := gateE2ETemplate(t, gateCfg)
	f := setupWorkflowAPIFixture(t, "ac8-trunc", nodes, edges)
	runID := uuidToString(f.run.ID)

	workTask := f.stepTask(t, "work")
	w := postWorkDone(t, f.wh, workTask, f.executorID)
	if w.Code != http.StatusCreated {
		t.Fatalf("upstream submission = %d, want 201; body=%s", w.Code, w.Body.String())
	}

	if got := stepStatusForNodeKey(t, runID, "gate"); got != workflow.StepBlocked {
		t.Fatalf("gate step = %q, want blocked (truncated output → non-JSON → error)", got)
	}
	if got := runStatusForRun(t, runID); got != workflow.RunPaused && got != workflow.RunFailed {
		t.Fatalf("run = %q, want paused or failed", got)
	}
	if got := gateRunStatusEventually(t, runID, "gate"); got != "error" {
		t.Fatalf("gate_run.status = %v, want error (non-JSON last line)", got)
	}
	gr := gateRunForRunStep(t, runID, "gate")
	out, _ := gr["output"].(map[string]any)
	if out == nil {
		t.Fatalf("gate_run.output missing; raw=%s", gr["raw"])
	}
	if truncated, _ := out["truncated"].(bool); !truncated {
		t.Fatalf("gate_run.output.truncated = %v, want true (1MB cap hit)", out["truncated"])
	}
	// The captured stdout is non-empty and contains the cap's worth of
	// 'y' bytes — proves the limit writer kept the partial output for
	// audit rather than dropping the whole stream on the floor.
	if stdout, _ := out["stdout"].(string); len(stdout) == 0 {
		t.Fatalf("gate_run.output.stdout empty; want truncated partial stdout for audit")
	}
}

// ---------------------------------------------------------------------------
// AC9: fix_hint sanitization
// ---------------------------------------------------------------------------

// TestAC9ScriptGateFixHintSanitized pins PRD R6 contract #5: a fix_hint
// carrying control characters (ANSI ESC), CR, and embedded newlines is
// run through sanitizePromptText before storage, so terminal escapes
// cannot smuggle into the verdict payload or the downstream agent's
// prompt.
//
// The inline script uses `printf '%s\n'` rather than `echo` because
// /bin/sh on macOS interprets `\n` / `\r` inside echo's argument as
// actual control bytes, which would produce invalid JSON (literal
// newlines inside a JSON string). `printf '%s\n'` substitutes the
// argument verbatim into the format string, so the JSON source reaches
// the gate with `\n` / `\u001b` / `\r` intact as backslash escapes.
// Go's json.Unmarshal then decodes them into actual control bytes
// inside parsed.FixHint, and the P0 sanitizePromptText (rework.go:327)
// drops unicode.IsControl runes + folds unicode.IsSpace to single
// spaces, producing a single-line, escape-free string.
func TestAC9ScriptGateFixHintSanitized(t *testing.T) {
	// JSON literal: fix_hint contains backslash-escapes that must
	// reach json.Unmarshal verbatim. printf '%s\n' preserves them;
	// echo would have pre-interpreted \n / \r into real control bytes
	// and broken the JSON parse.
	gateCfg := workflow.NodeConfig{
		GateType: workflow.GateTypeScript,
		GateInlineScript: `printf '%s\n' '{"status":"block","fix_hint":"line1\n\u001b[31mRED\u001b[0m\r\nline2"}'`,
	}
	nodes, edges := gateE2ETemplate(t, gateCfg)
	f := setupWorkflowAPIFixture(t, "ac9-sanitize", nodes, edges)
	runID := uuidToString(f.run.ID)

	workTask := f.stepTask(t, "work")
	w := postWorkDone(t, f.wh, workTask, f.executorID)
	if w.Code != http.StatusCreated {
		t.Fatalf("upstream submission = %d, want 201; body=%s", w.Code, w.Body.String())
	}

	gr := gateRunForRunStep(t, runID, "gate")
	out, _ := gr["output"].(map[string]any)
	if out == nil {
		t.Fatalf("gate_run.output missing; raw=%s", gr["raw"])
	}
	fixHint, _ := out["fix_hint"].(string)
	if fixHint == "" {
		t.Fatalf("gate_run.output.fix_hint empty; want sanitized content")
	}
	if strings.Contains(fixHint, "\x1b") {
		t.Errorf("fix_hint leaked ANSI ESC: %q", fixHint)
	}
	if strings.Contains(fixHint, "\r") {
		t.Errorf("fix_hint leaked CR: %q", fixHint)
	}
	if strings.Contains(fixHint, "\n") {
		t.Errorf("fix_hint leaked newline: %q", fixHint)
	}
	// Sanity: the sanitized text still mentions both line contents
	// (control bytes are stripped but visible text survives).
	if !strings.Contains(fixHint, "line1") || !strings.Contains(fixHint, "line2") || !strings.Contains(fixHint, "RED") {
		t.Errorf("fix_hint dropped visible content: %q", fixHint)
	}
}

// ---------------------------------------------------------------------------
// AC10: P0/P1-1/P1-2 regression smoke
// ---------------------------------------------------------------------------

// TestAC10P0P1Regression is the smoke rail proving the gate node
// additions (NodeTypeGate constant + activate dispatch + NodeConfig
// fields + validateGateConfig) leave the P0 linear chain
// byte-identical. The full P0/P1-1/P1-2 AC suite runs as part of
// `make check`; this test is the inline rail that runs alongside the
// gate AC tests under `-run '^TestAC'`.
//
// Mirrors workflow_fanout_e2e_test.go's TestAC12P0Regression in shape:
// drive work → gate(evaluator agent) → review(acceptance) → end to
// completion, asserting every step reached passed. The "gate" node
// here is NodeTypeAgent with Role=evaluator (the P0 chain's pre-P1-3
// shape per seed.go deviation #4) — NodeTypeGate is exercised by the
// AC2-AC9 + AC11 tests above.
func TestAC10P0P1Regression(t *testing.T) {
	nodes, edges := e2eChainTemplate(t) // work → gate(evaluator) → review(acceptance) → end
	f := setupWorkflowAPIFixture(t, "ac10-p0-regression", nodes, edges)
	wh := f.wh
	wrh := runHandlerFor(f)
	runID := uuidToString(f.run.ID)

	postSubmission(t, wh, f.stepTask(t, "work"), f.executorID, workflow.SubmissionDone,
		map[string]any{"pr_url": "https://example/pr/ac10"})
	postVerdict(t, wh, f.stepTask(t, "gate"), f.evaluatorID, nil)
	if got := runStatusForRun(t, runID); got != workflow.RunWaitingAcceptance {
		t.Fatalf("run = %q, want waiting_acceptance (P0 chain intact)", got)
	}
	approveRunAcceptance(t, wrh, runID)
	if got := runStatusForRun(t, runID); got != workflow.RunCompleted {
		t.Fatalf("run = %q, want completed", got)
	}
	for _, node := range []string{"work", "gate", "review", "end"} {
		if got := stepStatusForRun(t, runID, node); got != workflow.StepPassed {
			t.Fatalf("P0 step %q = %q, want passed", node, got)
		}
	}
}

// ---------------------------------------------------------------------------
// AC11: flag-off — gate template does not weaken the feature-flag gate
// ---------------------------------------------------------------------------

// TestAC11GateFlagOff pins that adding the gate node to a published
// template does not weaken the P0 flag-off invariant (PRD AC11): with
// workflow_engine OFF, the inbound hook returns 404 (indistinguishable
// from an unknown token), and no run is created. The route-level gate
// is already covered by TestWorkflowRoutesGatedByFeatureFlag in
// cmd/server/workflow_gate_test.go and the listener no-op proofs by
// TestWorkflowListenerFlagOffIsNoOp in cmd/server/workflow_listeners_test.go
// — those tests do not depend on the template shape, so adding a gate
// template transitively keeps them green. This test is the gate-specific
// rail (mirrors workflow_fanout_e2e_test.go's TestAC11FanOutFlagOff).
func TestAC11GateFlagOff(t *testing.T) {
	gateCfg := workflow.NodeConfig{
		GateType:         workflow.GateTypeScript,
		GateInlineScript: `echo '{"status":"pass"}'`,
	}
	nodes, edges := gateE2ETemplate(t, gateCfg)
	f := setupHookFixture(t, "ac11-gate-flagoff", nodes, edges)

	// Rebuild the hook handler with the flag OFF.
	provider := featureflag.NewStaticProvider()
	provider.Set(workflow.FlagEngine, featureflag.Rule{Default: false})
	engine := workflow.NewEngine(testHandler.Queries, testPool, testHandler.IssueService, testHandler.TaskService, events.New())
	hh := NewWorkflowHookHandler(testHandler.Queries, engine, featureflag.NewService(provider), nil, nil, nil)

	// A valid token + valid payload still 404s under flag-off — the
	// hook handler checks the flag before revealing whether the token
	// exists (workflow_hook.go flag gate).
	w := httptest.NewRecorder()
	hh.HandleInboundHook(w, hookRequest(t, f.token, map[string]any{
		"title":     "AC11 gate flag-off push",
		"source_id": fmt.Sprintf("ac11-gate-src-%d", time.Now().UnixNano()),
	}))
	if w.Code != http.StatusNotFound {
		t.Fatalf("flag-off hook = %d, want 404 (AC11); body=%s", w.Code, w.Body.String())
	}

	// No run created.
	var runs int
	if err := testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM workflow_run WHERE workspace_id = $1 AND template_id = $2`,
		testWorkspaceID, f.templateID).Scan(&runs); err != nil {
		t.Fatalf("count runs: %v", err)
	}
	if runs != 0 {
		t.Fatalf("flag-off created %d runs, want 0 (listener no-op)", runs)
	}
}
