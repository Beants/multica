package handler

// workflow_gate_agent_e2e_test.go — P1-3b acceptance-criteria suite
// (implement.md Wave 2 step 2.2). One test per AC over the HTTP handler
// surface for the gate node's agent and adversarial forms (PRD §Goal +
// R1-R6).
//
// Canonical shape under test (agentGateE2ETemplate):
//
//	work(executor) → gate(gate_type=agent|adversarial) → end
//
// The work node declares no required exit fields so a DONE submission
// with an empty exit_fields map is schema-valid. When the upstream
// executor submission derives the system pass verdict, consumeVerdictTx
// activates the gate step, which dispatches to activateGateAgentNode
// (P0 evaluator-role agent pipeline with role=evaluator forced). The
// fixture's setupWorkflowAPIFixture auto-binds the gate to the
// evaluator agent when gate_type is agent or adversarial.
//
// P1-3b verdict matrix (PRD R5 / R6):
//
//	agent        verdict=pass                → step passed  + gate_run.pass
//	agent        verdict=fail + on_fail=block → step blocked + gate_run.block (default)
//	agent        verdict=fail + on_fail=warn  → step passed  + gate_run.warn
//	adversarial  verdict=fail + on_fail unset → step passed  + gate_run.warn (default warn)
//	adversarial  verdict=fail + on_fail=block → step blocked + gate_run.block (explicit)

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/workflow"
)

// ---------------------------------------------------------------------------
// Template helpers
// ---------------------------------------------------------------------------

// agentGateE2ETemplate mirrors gateE2ETemplate but targets the P1-3b
// agent / adversarial forms. The fixture replaces the work node's
// selector with the executor agent and the gate node's selector with
// the evaluator agent (setupWorkflowAPIFixture P1-3b branch).
func agentGateE2ETemplate(t *testing.T, gateCfg workflow.NodeConfig) ([]workflow.NodeInput, []workflow.EdgeInput) {
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

// postAgentGateVerdict posts a verdict on the gate step from the
// evaluator agent. Returns the recorder for status inspection.
func postAgentGateVerdict(t *testing.T, wh *WorkflowHandler, taskID, evaluatorID, result string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	wh.CreateVerdict(w, matRequest(t, "POST", "/api/tasks/"+taskID+"/verdict", taskID, evaluatorID, map[string]any{
		"result":      result,
		"exit_fields": map[string]any{},
	}))
	return w
}

// handoffNoteForStep reads the handoff_note text stored on the
// agent_task_queue row for the latest attempt of a node's step. Used
// by the adversarial context whitelist AC to assert the injected
// prompt framing.
func handoffNoteForStep(t *testing.T, runID, nodeKey string) string {
	t.Helper()
	var note string
	if err := testPool.QueryRow(context.Background(), `
		SELECT coalesce(atq.handoff_note, '')
		FROM agent_task_queue atq
		JOIN step_instance si ON si.agent_task_id = atq.id
		WHERE si.run_id = $1 AND si.node_key = $2
		ORDER BY si.attempt DESC, atq.created_at DESC LIMIT 1
	`, runID, nodeKey).Scan(&note); err != nil {
		t.Fatalf("read handoff note for %q: %v", nodeKey, err)
	}
	return note
}

// publishAgentGate attempts to create + publish the agent gate template
// directly through the template service (bypassing the fixture's
// auto-bind), used by the publish-time ACs to test refusals.
func publishAgentGate(t *testing.T, gateCfg workflow.NodeConfig) error {
	ctx := context.Background()
	nodes, edges := agentGateE2ETemplate(t, gateCfg)
	templates := workflow.NewTemplateService(testHandler.Queries, testPool)
	detail, err := templates.CreateTemplate(ctx, workflow.CreateTemplateParams{
		WorkspaceID: util.MustParseUUID(testWorkspaceID),
		Key:         fmt.Sprintf("p13b-publish-%d", time.Now().UnixNano()),
		Name:        "p13b-publish",
		CreatedBy:   util.MustParseUUID(testUserID),
		Nodes:       nodes, Edges: edges,
	})
	if err != nil {
		return err
	}
	_, perr := templates.PublishTemplate(ctx, util.MustParseUUID(testWorkspaceID), detail.Template.ID)
	return perr
}

// ---------------------------------------------------------------------------
// AC1: publish-time — agent / adversarial gate requires agent_selector
// ---------------------------------------------------------------------------

// TestAC1AgentGateMissingSelector pins PRD AC1: gate_type=agent or
// adversarial without agent_selector is refused at publish with a
// 422-equivalent error naming agent_selector. The publish loop skips
// selector resolution when there is nothing to resolve, so the refusal
// surfaces from validateGateConfig (the cheapest layer).
func TestAC1AgentGateMissingSelector(t *testing.T) {
	for _, gt := range []string{workflow.GateTypeAgent, workflow.GateTypeAdversarial} {
		t.Run(gt, func(t *testing.T) {
			err := publishAgentGate(t, workflow.NodeConfig{GateType: gt})
			if err == nil {
				t.Fatalf("publish without agent_selector: expected error, got nil")
			}
			if !strings.Contains(err.Error(), "agent_selector") {
				t.Fatalf("publish err = %v, want mention of agent_selector", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// AC2: publish-time — EvaluatorSeparation covers gate agent
// ---------------------------------------------------------------------------

// TestAC2AgentGateEvaluatorSeparation pins PRD AC2 + blueprint pillar 5:
// a gate node (gate_type=agent or adversarial) whose agent_selector
// resolves to the SAME agent as an upstream executor is refused at
// publish with EvaluatorSeparationError. The check is now applied to
// NodeTypeGate (gate_type in {agent, adversarial}) in addition to the
// P0 NodeTypeAgent + role=evaluator case.
//
// The template seeds BOTH the work and the gate with the same agent
// name; the fixture is bypassed (publishAgentGate) so setupWorkflowAPIFixture's
// auto-bind does not split them.
func TestAC2AgentGateEvaluatorSeparation(t *testing.T) {
	for _, gt := range []string{workflow.GateTypeAgent, workflow.GateTypeAdversarial} {
		t.Run(gt, func(t *testing.T) {
			ctx := context.Background()
			// Create a real agent that BOTH the work (executor) and the
			// gate (evaluator-form) will reference. EvaluatorSeparation
			// fires when the publish-time resolution maps them to the
			// SAME agent ID.
			sharedName := fmt.Sprintf("Shared Agent %d", time.Now().UnixNano())
			var sharedID string
			if err := testPool.QueryRow(ctx, `
				INSERT INTO agent (
					workspace_id, name, description, runtime_mode, runtime_config,
					runtime_id, visibility, max_concurrent_tasks, owner_id
				) VALUES ($1, $2, '', 'cloud', '{}'::jsonb, $3, 'private', 1, $4)
				RETURNING id
			`, testWorkspaceID, sharedName, testRuntimeID, testUserID).Scan(&sharedID); err != nil {
				t.Fatalf("create shared agent: %v", err)
			}
			t.Cleanup(func() {
				testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE agent_id = $1`, sharedID)
				testPool.Exec(ctx, `DELETE FROM agent WHERE id = $1`, sharedID)
			})

			work := workflow.NodeInput{
				NodeKey: "work", Type: workflow.NodeTypeAgent, Name: "work",
				Config: agentCfg(t, workflow.NodeConfig{
					Role:          workflow.RoleExecutor,
					AgentSelector: sharedName,
				}),
			}
			gate := workflow.NodeInput{
				NodeKey: "gate", Type: workflow.NodeTypeGate, Name: "gate",
				Config: agentCfg(t, workflow.NodeConfig{
					GateType:      gt,
					AgentSelector: sharedName,
				}),
			}
			end := workflow.NodeInput{
				NodeKey: "end", Type: workflow.NodeTypeEnd, Name: "end",
				Config: agentCfg(t, workflow.NodeConfig{}),
			}
			templates := workflow.NewTemplateService(testHandler.Queries, testPool)
			detail, err := templates.CreateTemplate(ctx, workflow.CreateTemplateParams{
				WorkspaceID: util.MustParseUUID(testWorkspaceID),
				Key:         fmt.Sprintf("p13b-sep-%d-%s", time.Now().UnixNano(), gt),
				Name:        "p13b-sep",
				CreatedBy:   util.MustParseUUID(testUserID),
				Nodes:       []workflow.NodeInput{work, gate, end},
				Edges: []workflow.EdgeInput{
					{FromNodeKey: "work", ToNodeKey: "gate"},
					{FromNodeKey: "gate", ToNodeKey: "end"},
				},
			})
			if err != nil {
				t.Fatalf("create template: %v", err)
			}
			t.Cleanup(func() {
				testPool.Exec(ctx, `DELETE FROM workflow_template WHERE id = $1`, detail.Template.ID)
			})
			_, perr := templates.PublishTemplate(ctx, util.MustParseUUID(testWorkspaceID), detail.Template.ID)
			if perr == nil {
				t.Fatalf("publish with shared executor+gate agent: expected EvaluatorSeparationError, got nil")
			}
			if !strings.Contains(perr.Error(), "evaluator") || !strings.Contains(perr.Error(), "upstream executor") {
				t.Fatalf("publish err = %v, want EvaluatorSeparationError", perr)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// AC3: agent gate verdict=pass → step passed + gate_run.pass
// ---------------------------------------------------------------------------

// TestAC3AgentGatePass pins the agent-form happy path (PRD AC3): an
// evaluator agent's pass verdict drives the gate step to passed, the
// gate_run row to status=pass, and the run to completed (the end tail
// activates once the gate verdict is StepPassed).
func TestAC3AgentGatePass(t *testing.T) {
	gateCfg := workflow.NodeConfig{GateType: workflow.GateTypeAgent, AgentSelector: "WF Placeholder Evaluator"}
	nodes, edges := agentGateE2ETemplate(t, gateCfg)
	f := setupWorkflowAPIFixture(t, "p13b-ac3-pass", nodes, edges)
	runID := uuidToString(f.run.ID)

	// Drive the upstream executor to DONE → gate step activates.
	workTask := f.stepTask(t, "work")
	w := postWorkDone(t, f.wh, workTask, f.executorID)
	if w.Code != http.StatusCreated {
		t.Fatalf("upstream submission = %d, want 201; body=%s", w.Code, w.Body.String())
	}

	// gate step → active (gate agent dispatched, awaiting verdict).
	if got := stepStatusForNodeKey(t, runID, "gate"); got != workflow.StepActive {
		t.Fatalf("gate step = %q, want active after work passed", got)
	}
	// gate_run row exists in 'running' state.
	gr := gateRunForRunStep(t, runID, "gate")
	if got := gr["status"]; got != "running" {
		t.Fatalf("gate_run.status pre-verdict = %v, want running", got)
	}
	if got := gr["gate_type"]; got != workflow.GateTypeAgent {
		t.Fatalf("gate_run.gate_type = %v, want %q", got, workflow.GateTypeAgent)
	}

	// Evaluator pass verdict → step passed + gate_run finalized.
	gateTask := f.stepTask(t, "gate")
	w = postAgentGateVerdict(t, f.wh, gateTask, f.evaluatorID, workflow.VerdictPass)
	if w.Code != http.StatusCreated {
		t.Fatalf("verdict = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	if got := stepStatusForNodeKey(t, runID, "gate"); got != workflow.StepPassed {
		t.Fatalf("gate step = %q, want passed", got)
	}
	// gate_run row → pass (finalized by consumeVerdictTx hook).
	gr = gateRunForRunStep(t, runID, "gate")
	if got := gr["status"]; got != "pass" {
		t.Fatalf("gate_run.status post-verdict = %v, want pass", got)
	}
	// Run completes via the end node.
	if got := runStatusForRun(t, runID); got != workflow.RunCompleted {
		t.Fatalf("run = %q, want completed (gate pass advances the chain)", got)
	}
}

// ---------------------------------------------------------------------------
// AC4: agent gate verdict=fail + on_fail=block → step blocked + paused
// ---------------------------------------------------------------------------

// TestAC4AgentGateBlock pins the agent-form block path (PRD AC4): an
// evaluator agent's fail verdict with gate_on_fail=block (the default)
// drives the gate step to blocked, the run to paused, gate_run.status
// to block, and notifies the reviewer via inbox.
func TestAC4AgentGateBlock(t *testing.T) {
	gateCfg := workflow.NodeConfig{
		GateType:      workflow.GateTypeAgent,
		AgentSelector: "WF Placeholder Evaluator",
		// GateOnFail unset → EffectiveGateOnFail() returns block for agent form.
	}
	nodes, edges := agentGateE2ETemplate(t, gateCfg)
	f := setupWorkflowAPIFixture(t, "p13b-ac4-block", nodes, edges)
	runID := uuidToString(f.run.ID)

	workTask := f.stepTask(t, "work")
	w := postWorkDone(t, f.wh, workTask, f.executorID)
	if w.Code != http.StatusCreated {
		t.Fatalf("upstream submission = %d, want 201; body=%s", w.Code, w.Body.String())
	}

	gateTask := f.stepTask(t, "gate")
	// verdict=fail + default on_fail=block → step blocked + run paused.
	w = postAgentGateVerdict(t, f.wh, gateTask, f.evaluatorID, workflow.VerdictFail)
	if w.Code != http.StatusCreated {
		t.Fatalf("verdict = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	if got := stepStatusForNodeKey(t, runID, "gate"); got != workflow.StepBlocked {
		t.Fatalf("gate step = %q, want blocked (fail + on_fail=block)", got)
	}
	if got := runStatusForRun(t, runID); got != workflow.RunPaused && got != workflow.RunFailed {
		t.Fatalf("run = %q, want paused or failed (gate blocked)", got)
	}
	gr := gateRunForRunStep(t, runID, "gate")
	if got := gr["status"]; got != "block" {
		t.Fatalf("gate_run.status = %v, want block (fail + on_fail=block)", got)
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
// AC5: adversarial context whitelist in handoff_note
// ---------------------------------------------------------------------------

// TestAC5AdversarialContextWhitelist pins PRD AC5: gate_type=adversarial
// produces a handoff_note that whitelists node identity + exit_fields
// schema only. The note must NOT contain instructions, upstream
// exit_fields, or rework framing — the adversarial reviewer must derive
// its verdict from the diff/test cases the daemon exposes via the
// workdir, not from the builder's instructions (squad-briefing.md:158).
func TestAC5AdversarialContextWhitelist(t *testing.T) {
	gateCfg := workflow.NodeConfig{
		GateType:      workflow.GateTypeAdversarial,
		AgentSelector: "WF Placeholder Evaluator",
		Instructions:  "Top-secret builder-supplied instructions that must NOT leak.",
	}
	// Add a required exit field on the upstream node so the non-adversarial
	// buildHandoffNote path WOULD inject an `[upstream exit_fields from ...]`
	// line — its absence under adversarial is the AC.
	nodes, edges := gateE2ETemplateWithUpstreamField(t, gateCfg, "pr_url", "string")
	f := setupWorkflowAPIFixture(t, "p13b-ac5-whitelist", nodes, edges)
	runID := uuidToString(f.run.ID)

	// Drive upstream to DONE with a populated exit field so a non-whitelisted
	// buildHandoffNote would have something to inject.
	workTask := f.stepTask(t, "work")
	w := httptest.NewRecorder()
	f.wh.CreateSubmission(w, matRequest(t, "POST", "/api/tasks/"+workTask+"/submission", workTask, f.executorID, map[string]any{
		"status":      workflow.SubmissionDone,
		"exit_fields": map[string]any{"pr_url": "https://example/pr/leak"},
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("upstream submission = %d, want 201; body=%s", w.Code, w.Body.String())
	}

	// gate step active = handoff note was already built + enqueued.
	if got := stepStatusForNodeKey(t, runID, "gate"); got != workflow.StepActive {
		t.Fatalf("gate step = %q, want active (handoff built + dispatched)", got)
	}
	note := handoffNoteForStep(t, runID, "gate")
	if note == "" {
		t.Fatalf("handoff_note empty; expected adversarial whitelisted note")
	}
	// Whitelist: identity + adversarial framing must be present.
	if !strings.Contains(note, "[workflow node]") {
		t.Errorf("handoff_note missing [workflow node]:\n%s", note)
	}
	if !strings.Contains(note, "Adversarial review:") {
		t.Errorf("handoff_note missing Adversarial review framing:\n%s", note)
	}
	// Skip-list: builder-supplied instructions must NOT leak.
	if strings.Contains(note, "[instructions]") {
		t.Errorf("handoff_note leaked [instructions] under adversarial whitelist:\n%s", note)
	}
	if strings.Contains(note, "Top-secret builder-supplied") {
		t.Errorf("handoff_note leaked builder instructions text:\n%s", note)
	}
	if strings.Contains(note, "[upstream") {
		t.Errorf("handoff_note leaked [upstream exit_fields] under adversarial whitelist:\n%s", note)
	}
	if strings.Contains(note, "pr_url") {
		t.Errorf("handoff_note leaked upstream exit field value:\n%s", note)
	}
	if strings.Contains(note, "[rework]") {
		t.Errorf("handoff_note leaked [rework] framing:\n%s", note)
	}
}

// ---------------------------------------------------------------------------
// AC6: adversarial default on_fail=warn → fail does not block
// ---------------------------------------------------------------------------

// TestAC6AdversarialDefaultWarn pins PRD AC6 + harness practice
// (squad-briefing.md:158): gate_type=adversarial with unset on_fail
// defaults to warn via EffectiveGateOnFail, so a fail verdict still
// advances the chain — adversarial review is advisory, not blocking.
// gate_run.status records warn (the verdict's verdict); the run
// completes through the end node.
func TestAC6AdversarialDefaultWarn(t *testing.T) {
	gateCfg := workflow.NodeConfig{
		GateType:      workflow.GateTypeAdversarial,
		AgentSelector: "WF Placeholder Evaluator",
		// GateOnFail unset → EffectiveGateOnFail() returns warn for adversarial.
	}
	nodes, edges := agentGateE2ETemplate(t, gateCfg)
	f := setupWorkflowAPIFixture(t, "p13b-ac6-warn", nodes, edges)
	runID := uuidToString(f.run.ID)

	workTask := f.stepTask(t, "work")
	w := postWorkDone(t, f.wh, workTask, f.executorID)
	if w.Code != http.StatusCreated {
		t.Fatalf("upstream submission = %d, want 201; body=%s", w.Code, w.Body.String())
	}

	// verdict=fail + default on_fail=warn → step still passed (advisory).
	gateTask := f.stepTask(t, "gate")
	w = postAgentGateVerdict(t, f.wh, gateTask, f.evaluatorID, workflow.VerdictFail)
	if w.Code != http.StatusCreated {
		t.Fatalf("verdict = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	if got := stepStatusForNodeKey(t, runID, "gate"); got != workflow.StepPassed {
		t.Fatalf("gate step = %q, want passed (adversarial default warn promotes fail)", got)
	}
	// gate_run.status = warn (derived from fail + on_fail=warn).
	gr := gateRunForRunStep(t, runID, "gate")
	if got := gr["status"]; got != "warn" {
		t.Fatalf("gate_run.status = %v, want warn (fail + on_fail=warn)", got)
	}
	if got := gr["gate_type"]; got != workflow.GateTypeAdversarial {
		t.Fatalf("gate_run.gate_type = %v, want %q", got, workflow.GateTypeAdversarial)
	}
	// Run completes through end → completed.
	if got := runStatusForRun(t, runID); got != workflow.RunCompleted {
		t.Fatalf("run = %q, want completed (adversarial default warn does not pause)", got)
	}
}

// ---------------------------------------------------------------------------
// AC7: gate_run finalized to terminal status on verdict
// ---------------------------------------------------------------------------

// TestAC7GateRunSyncedOnVerdict pins PRD AC7: the consumeVerdictTx hook
// finalizes a still-running gate_run when the step reaches a terminal
// state. Covers the three terminal verdict outcomes (pass / fail /
// blocked) and asserts gate_run.finished_at is populated for each.
func TestAC7GateRunSyncedOnVerdict(t *testing.T) {
	cases := []struct {
		name           string
		gateType       string
		gateOnFail     string
		verdictResult  string
		wantStepStatus string
		wantRunStatus  string
		wantGateStatus string
	}{
		{
			name:           "agent_pass",
			gateType:       workflow.GateTypeAgent,
			verdictResult:  workflow.VerdictPass,
			wantStepStatus: workflow.StepPassed,
			wantRunStatus:  workflow.RunCompleted,
			wantGateStatus: "pass",
		},
		{
			name:           "agent_fail_block",
			gateType:       workflow.GateTypeAgent,
			gateOnFail:     workflow.GateOnFailBlock,
			verdictResult:  workflow.VerdictFail,
			wantStepStatus: workflow.StepBlocked,
			wantRunStatus:  workflow.RunPaused,
			wantGateStatus: "block",
		},
		{
			name:           "agent_blocked_verdict",
			gateType:       workflow.GateTypeAgent,
			gateOnFail:     workflow.GateOnFailBlock,
			verdictResult:  workflow.VerdictBlocked,
			wantStepStatus: workflow.StepBlocked,
			wantRunStatus:  workflow.RunPaused,
			wantGateStatus: "block",
		},
		{
			name:           "adversarial_default_warn",
			gateType:       workflow.GateTypeAdversarial,
			verdictResult:  workflow.VerdictFail,
			wantStepStatus: workflow.StepPassed,
			wantRunStatus:  workflow.RunCompleted,
			wantGateStatus: "warn",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gateCfg := workflow.NodeConfig{
				GateType:      tc.gateType,
				AgentSelector: "WF Placeholder Evaluator",
				GateOnFail:    tc.gateOnFail,
			}
			nodes, edges := agentGateE2ETemplate(t, gateCfg)
			f := setupWorkflowAPIFixture(t, "p13b-ac7-"+tc.name, nodes, edges)
			runID := uuidToString(f.run.ID)

			workTask := f.stepTask(t, "work")
			w := postWorkDone(t, f.wh, workTask, f.executorID)
			if w.Code != http.StatusCreated {
				t.Fatalf("upstream submission = %d, want 201; body=%s", w.Code, w.Body.String())
			}

			// gate_run in 'running' before the verdict lands.
			gr := gateRunForRunStep(t, runID, "gate")
			if got := gr["status"]; got != "running" {
				t.Fatalf("pre-verdict gate_run.status = %v, want running", got)
			}

			// finished_at must be NULL pre-verdict (CreateGateRun does
			// not set it).
			var finishedAt *time.Time
			if err := testPool.QueryRow(context.Background(), `
				SELECT gr.finished_at FROM gate_run gr
				JOIN step_instance si ON si.id = gr.step_instance_id
				WHERE si.run_id = $1 AND si.node_key = 'gate'
				ORDER BY gr.created_at DESC LIMIT 1
			`, runID).Scan(&finishedAt); err != nil {
				t.Fatalf("read gate_run.finished_at pre-verdict: %v", err)
			}
			if finishedAt != nil {
				t.Fatalf("pre-verdict finished_at = %v, want NULL", finishedAt)
			}

			gateTask := f.stepTask(t, "gate")
			w = postAgentGateVerdict(t, f.wh, gateTask, f.evaluatorID, tc.verdictResult)
			if w.Code != http.StatusCreated {
				t.Fatalf("verdict = %d, want 201; body=%s", w.Code, w.Body.String())
			}
			if got := stepStatusForNodeKey(t, runID, "gate"); got != tc.wantStepStatus {
				t.Fatalf("gate step = %q, want %q", got, tc.wantStepStatus)
			}
			if got := runStatusForRun(t, runID); got != tc.wantRunStatus &&
				!(tc.wantRunStatus == workflow.RunPaused && got == workflow.RunFailed) {
				t.Fatalf("run = %q, want %q", got, tc.wantRunStatus)
			}
			gr = gateRunForRunStep(t, runID, "gate")
			if got := gr["status"]; got != tc.wantGateStatus {
				t.Fatalf("gate_run.status = %v, want %q", got, tc.wantGateStatus)
			}
			// finished_at must be populated post-verdict (UpdateGateRunResult
			// sets finished_at = now()).
			if err := testPool.QueryRow(context.Background(), `
				SELECT gr.finished_at FROM gate_run gr
				JOIN step_instance si ON si.id = gr.step_instance_id
				WHERE si.run_id = $1 AND si.node_key = 'gate'
				ORDER BY gr.created_at DESC LIMIT 1
			`, runID).Scan(&finishedAt); err != nil {
				t.Fatalf("read gate_run.finished_at post-verdict: %v", err)
			}
			if finishedAt == nil {
				t.Fatalf("post-verdict finished_at = NULL, want populated")
			}
			// gate_run.output carries the agent's verdict_by + the
			// verdict id for the audit trail.
			out, _ := gr["output"].(map[string]any)
			if out == nil {
				t.Fatalf("gate_run.output missing; raw=%s", gr["raw"])
			}
			if _, ok := out["verdict_id"]; !ok {
				t.Errorf("gate_run.output.verdict_id missing: %v", out)
			}
			if got, _ := out["verdict_by"].(string); got != "agent" {
				t.Errorf("gate_run.output.verdict_by = %q, want \"agent\"", got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// AC8: regression smoke — P0 evaluator-agent chain still completes
// ---------------------------------------------------------------------------

// TestAC8AgentGateP0Regression is the smoke rail proving the P1-3b
// additions (gate agent dispatch + adversarial handoff whitelist +
// consumeVerdictTx gate_run hook) leave the P0 evaluator-agent chain
// byte-identical. The full P0/P1-1/P1-2/P1-3 AC suite runs as part of
// `make check`; this test is the inline rail that runs alongside the
// P1-3b AC tests under `-run '^TestAC'`.
//
// The chain is the P0 form: work(executor) → gate(evaluator NodeTypeAgent)
// → review(acceptance) → end. NodeTypeAgent with role=evaluator is the
// original P0 gate-equivalent per seed.go deviation #4 (the P1-3
// NodeTypeGate script form is covered by workflow_gate_e2e_test.go;
// the agent/adversarial forms are covered by AC1-AC7 above).
func TestAC8AgentGateP0Regression(t *testing.T) {
	nodes, edges := e2eChainTemplate(t) // work → gate(evaluator) → review(acceptance) → end
	f := setupWorkflowAPIFixture(t, "p13b-ac8-p0-regression", nodes, edges)
	wh := f.wh
	wrh := runHandlerFor(f)
	runID := uuidToString(f.run.ID)

	postSubmission(t, wh, f.stepTask(t, "work"), f.executorID, workflow.SubmissionDone,
		map[string]any{"pr_url": "https://example/pr/p13b-ac8"})
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
	// No gate_run row should exist for the NodeTypeAgent gate — the
	// P1-3b hook only finalizes rows created by activateGateAgentNode,
	// and the P0 evaluator-agent path does not create one.
	var gateRunCount int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM gate_run gr
		JOIN step_instance si ON si.id = gr.step_instance_id
		WHERE si.run_id = $1
	`, runID).Scan(&gateRunCount); err != nil {
		t.Fatalf("count gate_run: %v", err)
	}
	if gateRunCount != 0 {
		t.Fatalf("gate_run rows = %d, want 0 (P0 evaluator-agent path does not create gate_run)", gateRunCount)
	}
}

// ---------------------------------------------------------------------------
// Extra: adversarial explicit on_fail=block DOES block (boundary check)
// ---------------------------------------------------------------------------

// TestAdversarialExplicitBlockOverridesDefault is a boundary test for
// EffectiveGateOnFail: an adversarial gate with explicit on_fail=block
// honors the explicit value (the default warn does NOT override an
// explicit block). Mirrors the TestNodeConfigEffectiveGateOnFailAdversarial
// unit-level assertion at the e2e level.
func TestAdversarialExplicitBlockOverridesDefault(t *testing.T) {
	gateCfg := workflow.NodeConfig{
		GateType:      workflow.GateTypeAdversarial,
		AgentSelector: "WF Placeholder Evaluator",
		GateOnFail:    workflow.GateOnFailBlock,
	}
	nodes, edges := agentGateE2ETemplate(t, gateCfg)
	f := setupWorkflowAPIFixture(t, "p13b-adv-explicit-block", nodes, edges)
	runID := uuidToString(f.run.ID)

	workTask := f.stepTask(t, "work")
	w := postWorkDone(t, f.wh, workTask, f.executorID)
	if w.Code != http.StatusCreated {
		t.Fatalf("upstream submission = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	gateTask := f.stepTask(t, "gate")
	w = postAgentGateVerdict(t, f.wh, gateTask, f.evaluatorID, workflow.VerdictFail)
	if w.Code != http.StatusCreated {
		t.Fatalf("verdict = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	if got := stepStatusForNodeKey(t, runID, "gate"); got != workflow.StepBlocked {
		t.Fatalf("gate step = %q, want blocked (explicit on_fail=block wins over adversarial default)", got)
	}
	gr := gateRunForRunStep(t, runID, "gate")
	if got := gr["status"]; got != "block" {
		t.Fatalf("gate_run.status = %v, want block (explicit on_fail=block)", got)
	}
}
