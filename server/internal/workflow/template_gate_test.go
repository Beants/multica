package workflow

// template_gate_test.go — P1-3 Wave 2 unit tests for validateGateConfig
// + the NodeConfig plumbing (Effective* helpers, ParseNodeConfig enum).
// Pure unit tests (no DB) so they run in the fast lane alongside
// template_dag_test.go and template_fanout_test.go.
//
// Engine-backed coverage (activateGateNode end-to-end through the real
// double-tx flow) lands in Wave 3 (workflow_gate_e2e_test.go).

import (
	"encoding/json"
	"strings"
	"testing"
)

// gateNode builds a gate node input with the given key + config.
func gateNode(key string, cfg NodeConfig) NodeInput {
	raw, err := json.Marshal(cfg)
	if err != nil {
		panic(err)
	}
	return NodeInput{NodeKey: key, Type: NodeTypeGate, Name: key, Config: raw}
}

// chainEdges wraps a gate node between an upstream agent + downstream end
// so the structural validator (single start, no orphan, out-degree ≤ 1)
// does not reject the fixture before validateGateConfig runs.
func gateChainEdges(up, gate, down string) []EdgeInput {
	return []EdgeInput{
		{FromNodeKey: up, ToNodeKey: gate},
		{FromNodeKey: gate, ToNodeKey: down},
	}
}

// ---------------------------------------------------------------------------
// validateGateConfig: gate_type rules (AC2)
// ---------------------------------------------------------------------------

func TestValidateGateConfig_MissingGateType(t *testing.T) {
	t.Parallel()
	nodes := []NodeInput{
		agentNode("up", RoleExecutor, "Agent", NodeConfig{
			ExitFields: &ExitFieldsSchema{Fields: []ExitFieldSpec{{Name: "any", Type: "any"}}},
		}),
		gateNode("g", NodeConfig{GateInlineScript: `echo hi`}),
		typedNode("end", NodeTypeEnd, NodeConfig{}),
	}
	edges := gateChainEdges("up", "g", "end")
	err := validateTemplateGraph(nodes, edges)
	if err == nil || !strings.Contains(err.Error(), "requires gate_type") {
		t.Fatalf("got err = %v, want 'requires gate_type'", err)
	}
}

func TestValidateGateConfig_UnknownGateType(t *testing.T) {
	t.Parallel()
	nodes := []NodeInput{
		agentNode("up", RoleExecutor, "Agent", NodeConfig{}),
		gateNode("g", NodeConfig{GateType: "bogus"}),
		typedNode("end", NodeTypeEnd, NodeConfig{}),
	}
	edges := gateChainEdges("up", "g", "end")
	err := validateTemplateGraph(nodes, edges)
	if err == nil || !strings.Contains(err.Error(), "unknown gate_type") {
		t.Fatalf("got err = %v, want 'unknown gate_type'", err)
	}
}

// P1-3b forward-compat: agent/rules/adversarial/hybrid are accepted at
// publish (no script_ref/inline XOR check) so a template carrying them
// survives until P1-3b activates them.
func TestValidateGateConfig_P13bFormsAcceptedAtPublish(t *testing.T) {
	t.Parallel()
	for _, gt := range []string{GateTypeAgent, GateTypeRules, GateTypeAdversarial, GateTypeHybrid} {
		nodes := []NodeInput{
			agentNode("up", RoleExecutor, "Agent", NodeConfig{}),
			gateNode("g", NodeConfig{GateType: gt}),
			typedNode("end", NodeTypeEnd, NodeConfig{}),
		}
		edges := gateChainEdges("up", "g", "end")
		if err := validateTemplateGraph(nodes, edges); err != nil {
			t.Errorf("gate_type=%q should pass publish validation, got: %v", gt, err)
		}
	}
}

// ---------------------------------------------------------------------------
// validateGateConfig: gate_type=script source XOR (AC2)
// ---------------------------------------------------------------------------

func TestValidateGateConfig_ScriptMissingSource(t *testing.T) {
	t.Parallel()
	nodes := []NodeInput{
		agentNode("up", RoleExecutor, "Agent", NodeConfig{}),
		gateNode("g", NodeConfig{GateType: GateTypeScript}),
		typedNode("end", NodeTypeEnd, NodeConfig{}),
	}
	edges := gateChainEdges("up", "g", "end")
	err := validateTemplateGraph(nodes, edges)
	if err == nil || !strings.Contains(err.Error(), "exactly one of gate_script_ref or gate_inline_script") {
		t.Fatalf("got err = %v, want 'exactly one of gate_script_ref or gate_inline_script'", err)
	}
}

func TestValidateGateConfig_ScriptBothSource(t *testing.T) {
	t.Parallel()
	nodes := []NodeInput{
		agentNode("up", RoleExecutor, "Agent", NodeConfig{}),
		gateNode("g", NodeConfig{
			GateType:         GateTypeScript,
			GateScriptRef:    "registered-script",
			GateInlineScript: `echo hi`,
		}),
		typedNode("end", NodeTypeEnd, NodeConfig{}),
	}
	edges := gateChainEdges("up", "g", "end")
	err := validateTemplateGraph(nodes, edges)
	if err == nil || !strings.Contains(err.Error(), "exactly one of gate_script_ref or gate_inline_script") {
		t.Fatalf("got err = %v, want 'exactly one of gate_script_ref or gate_inline_script'", err)
	}
}

func TestValidateGateConfig_ScriptInlineOK(t *testing.T) {
	t.Parallel()
	nodes := []NodeInput{
		agentNode("up", RoleExecutor, "Agent", NodeConfig{}),
		gateNode("g", NodeConfig{
			GateType:         GateTypeScript,
			GateInlineScript: `echo '{"status":"pass"}'`,
		}),
		typedNode("end", NodeTypeEnd, NodeConfig{}),
	}
	edges := gateChainEdges("up", "g", "end")
	if err := validateTemplateGraph(nodes, edges); err != nil {
		t.Fatalf("inline-only gate should validate: %v", err)
	}
}

func TestValidateGateConfig_ScriptRefOK(t *testing.T) {
	t.Parallel()
	nodes := []NodeInput{
		agentNode("up", RoleExecutor, "Agent", NodeConfig{}),
		gateNode("g", NodeConfig{
			GateType:      GateTypeScript,
			GateScriptRef: "registered-script",
		}),
		typedNode("end", NodeTypeEnd, NodeConfig{}),
	}
	edges := gateChainEdges("up", "g", "end")
	if err := validateTemplateGraph(nodes, edges); err != nil {
		t.Fatalf("ref-only gate should validate: %v", err)
	}
}

// ---------------------------------------------------------------------------
// validateGateConfig: inline size cap (AC2)
// ---------------------------------------------------------------------------

func TestValidateGateConfig_InlineOverSize(t *testing.T) {
	t.Parallel()
	tooBig := strings.Repeat("x", gateInlineScriptMaxBytes+1)
	nodes := []NodeInput{
		agentNode("up", RoleExecutor, "Agent", NodeConfig{}),
		gateNode("g", NodeConfig{
			GateType:         GateTypeScript,
			GateInlineScript: tooBig,
		}),
		typedNode("end", NodeTypeEnd, NodeConfig{}),
	}
	edges := gateChainEdges("up", "g", "end")
	err := validateTemplateGraph(nodes, edges)
	if err == nil || !strings.Contains(err.Error(), "gate_inline_script") || !strings.Contains(err.Error(), "max") {
		t.Fatalf("got err = %v, want 'gate_inline_script ... max'", err)
	}
}

func TestValidateGateConfig_InlineAtSizeCap(t *testing.T) {
	t.Parallel()
	atCap := strings.Repeat("x", gateInlineScriptMaxBytes)
	nodes := []NodeInput{
		agentNode("up", RoleExecutor, "Agent", NodeConfig{}),
		gateNode("g", NodeConfig{
			GateType:         GateTypeScript,
			GateInlineScript: atCap,
		}),
		typedNode("end", NodeTypeEnd, NodeConfig{}),
	}
	edges := gateChainEdges("up", "g", "end")
	if err := validateTemplateGraph(nodes, edges); err != nil {
		t.Fatalf("inline at exactly %d bytes should validate: %v", gateInlineScriptMaxBytes, err)
	}
}

// ---------------------------------------------------------------------------
// validateGateConfig: language / timeout / on_fail (AC2)
// ---------------------------------------------------------------------------

func TestValidateGateConfig_UnknownLanguage(t *testing.T) {
	t.Parallel()
	nodes := []NodeInput{
		agentNode("up", RoleExecutor, "Agent", NodeConfig{}),
		gateNode("g", NodeConfig{
			GateType:         GateTypeScript,
			GateInlineScript: `echo hi`,
			GateLanguage:     "ruby",
		}),
		typedNode("end", NodeTypeEnd, NodeConfig{}),
	}
	edges := gateChainEdges("up", "g", "end")
	err := validateTemplateGraph(nodes, edges)
	if err == nil || !strings.Contains(err.Error(), "unknown gate_language") {
		t.Fatalf("got err = %v, want 'unknown gate_language'", err)
	}
}

func TestValidateGateConfig_LanguageShellAndPython3(t *testing.T) {
	t.Parallel()
	for _, lang := range []string{GateLanguageShell, GateLanguagePython3} {
		nodes := []NodeInput{
			agentNode("up", RoleExecutor, "Agent", NodeConfig{}),
			gateNode("g", NodeConfig{
				GateType:         GateTypeScript,
				GateInlineScript: `pass`,
				GateLanguage:     lang,
			}),
			typedNode("end", NodeTypeEnd, NodeConfig{}),
		}
		edges := gateChainEdges("up", "g", "end")
		if err := validateTemplateGraph(nodes, edges); err != nil {
			t.Errorf("gate_language=%q should validate: %v", lang, err)
		}
	}
}

func TestValidateGateConfig_TimeoutOutOfRange(t *testing.T) {
	t.Parallel()
	// validateGateConfig rejects out-of-range gate_timeout_seconds; 0 is
	// the "unset" sentinel (handled by EffectiveGateTimeoutSeconds → 60).
	cases := []struct {
		name string
		sec  int32
		ok   bool
	}{
		{"unset_zero", 0, true}, // 0 = unset → default
		{"min_1", 1, true},
		{"max_300", 300, true},
		{"above_max_301", 301, false},
	}
	for _, tc := range cases {
		nodes := []NodeInput{
			agentNode("up", RoleExecutor, "Agent", NodeConfig{}),
			gateNode("g", NodeConfig{
				GateType:           GateTypeScript,
				GateInlineScript:   `echo hi`,
				GateTimeoutSeconds: tc.sec,
			}),
			typedNode("end", NodeTypeEnd, NodeConfig{}),
		}
		edges := gateChainEdges("up", "g", "end")
		err := validateTemplateGraph(nodes, edges)
		if tc.ok && err != nil {
			t.Errorf("%s: want nil, got %v", tc.name, err)
		}
		if !tc.ok && (err == nil || !strings.Contains(err.Error(), "gate_timeout_seconds")) {
			t.Errorf("%s: want 'gate_timeout_seconds' error, got %v", tc.name, err)
		}
	}
}

// ParseNodeConfig covers gate_on_fail enum (the only gate field that
// parses cleanly without cross-node context).

func TestParseNodeConfig_GateOnFailEnum(t *testing.T) {
	t.Parallel()
	for _, onFail := range []string{"", GateOnFailBlock, GateOnFailWarn} {
		raw, _ := json.Marshal(NodeConfig{GateOnFail: onFail})
		if _, err := ParseNodeConfig(raw); err != nil {
			t.Errorf("gate_on_fail=%q should parse: %v", onFail, err)
		}
	}
	raw, _ := json.Marshal(NodeConfig{GateOnFail: "bogus"})
	if _, err := ParseNodeConfig(raw); err == nil || !strings.Contains(err.Error(), "unknown gate_on_fail") {
		t.Fatalf("gate_on_fail=bogus want 'unknown gate_on_fail' error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Effective* helpers
// ---------------------------------------------------------------------------

func TestNodeConfigEffectiveGateDefaults(t *testing.T) {
	t.Parallel()
	c := NodeConfig{}
	if got := c.EffectiveGateLanguage(); got != GateLanguageShell {
		t.Errorf("EffectiveGateLanguage() = %q, want %q", got, GateLanguageShell)
	}
	if got := c.EffectiveGateTimeoutSeconds(); got != int32(gateDefaultTimeoutSec) {
		t.Errorf("EffectiveGateTimeoutSeconds() = %d, want %d", got, gateDefaultTimeoutSec)
	}
	if got := c.EffectiveGateOnFail(); got != GateOnFailBlock {
		t.Errorf("EffectiveGateOnFail() = %q, want %q", got, GateOnFailBlock)
	}
	// Explicit values are returned as-is.
	c = NodeConfig{
		GateLanguage:       GateLanguagePython3,
		GateTimeoutSeconds: 120,
		GateOnFail:         GateOnFailWarn,
	}
	if got := c.EffectiveGateLanguage(); got != GateLanguagePython3 {
		t.Errorf("EffectiveGateLanguage() = %q, want %q", got, GateLanguagePython3)
	}
	if got := c.EffectiveGateTimeoutSeconds(); got != 120 {
		t.Errorf("EffectiveGateTimeoutSeconds() = %d, want 120", got)
	}
	if got := c.EffectiveGateOnFail(); got != GateOnFailWarn {
		t.Errorf("EffectiveGateOnFail() = %q, want %q", got, GateOnFailWarn)
	}
}

// ---------------------------------------------------------------------------
// parseGateOutput + deriveGateStatusAndVerdict (logic-only, no DB)
// ---------------------------------------------------------------------------

func TestParseGateOutput(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		stdout string
		ok     bool
		status string
	}{
		{"empty", "", false, ""},
		{"non_json_last_line", "hello world\n", false, ""},
		{"json_no_status", `{"fix_hint":"x"}` + "\n", false, ""},
		{"json_bad_status", `{"status":"bogus"}` + "\n", false, ""},
		{"pass_minimal", `echo stuff` + "\n" + `{"status":"pass"}` + "\n", true, "pass"},
		{"block_with_hint", `{"status":"block","fix_hint":"missing tests"}` + "\n", true, "block"},
		{"warn", `{"status":"warn","facts":["flaky"]}` + "\n", true, "warn"},
		{"json_with_trailing_whitespace", "  " + `{"status":"pass"}` + "   \n", true, "pass"},
	}
	for _, tc := range cases {
		got, ok := parseGateOutput(tc.stdout)
		if ok != tc.ok {
			t.Errorf("%s: ok=%v want %v", tc.name, ok, tc.ok)
			continue
		}
		if ok && got.Status != tc.status {
			t.Errorf("%s: status=%q want %q", tc.name, got.Status, tc.status)
		}
	}
}

func TestDeriveGateStatusAndVerdict(t *testing.T) {
	t.Parallel()
	// error path: any runErr → status=error + StepBlocked
	res := gateExecResult{stdout: "ignored"}
	status, gro, verdict := deriveGateStatusAndVerdict(res, errSentinel("boom"), GateOnFailBlock)
	if status != "error" || verdict != StepBlocked {
		t.Errorf("runErr path: status=%q verdict=%q want error/StepBlocked", status, verdict)
	}
	if gro.Stdout != "ignored" {
		t.Errorf("runErr path: stdout lost = %q", gro.Stdout)
	}

	// non-JSON last line → error + StepBlocked
	res = gateExecResult{stdout: "hello\nworld\n"}
	status, _, verdict = deriveGateStatusAndVerdict(res, nil, GateOnFailBlock)
	if status != "error" || verdict != StepBlocked {
		t.Errorf("non-JSON path: status=%q verdict=%q want error/StepBlocked", status, verdict)
	}

	// pass → StepPassed
	res = gateExecResult{stdout: `{"status":"pass"}` + "\n"}
	status, _, verdict = deriveGateStatusAndVerdict(res, nil, GateOnFailBlock)
	if status != "pass" || verdict != StepPassed {
		t.Errorf("pass: status=%q verdict=%q want pass/StepPassed", status, verdict)
	}

	// warn → StepPassed (advisory, never blocks)
	res = gateExecResult{stdout: `{"status":"warn"}` + "\n"}
	status, _, verdict = deriveGateStatusAndVerdict(res, nil, GateOnFailBlock)
	if status != "warn" || verdict != StepPassed {
		t.Errorf("warn: status=%q verdict=%q want warn/StepPassed", status, verdict)
	}

	// block + on_fail=block → StepBlocked
	res = gateExecResult{stdout: `{"status":"block","fix_hint":"fix me"}` + "\n"}
	status, gro, verdict = deriveGateStatusAndVerdict(res, nil, GateOnFailBlock)
	if status != "block" || verdict != StepBlocked {
		t.Errorf("block+block: status=%q verdict=%q want block/StepBlocked", status, verdict)
	}
	if gro.FixHint != "fix me" {
		t.Errorf("block+block: fix_hint lost = %q", gro.FixHint)
	}

	// block + on_fail=warn → StepPassed (evidence retained)
	res = gateExecResult{stdout: `{"status":"block","fix_hint":"noted"}` + "\n"}
	status, gro, verdict = deriveGateStatusAndVerdict(res, nil, GateOnFailWarn)
	if status != "block" || verdict != StepPassed {
		t.Errorf("block+warn: status=%q verdict=%q want block/StepPassed", status, verdict)
	}
	if gro.FixHint != "noted" {
		t.Errorf("block+warn: fix_hint lost = %q", gro.FixHint)
	}
}

// TestDeriveGateStatusAndVerdict_FixHintSanitized verifies the P0
// sanitizePromptText path runs on fix_hint (PRD R6 contract #5).
func TestDeriveGateStatusAndVerdict_FixHintSanitized(t *testing.T) {
	t.Parallel()
	// Raw-string JSON: \n / \u001b / \r are JSON escapes (not Go escape
	// sequences) — json.Unmarshal turns them into actual control bytes
	// in parsed.FixHint, then sanitizePromptText must strip/fold them.
	raw := `{"status":"block","fix_hint":"line1\nRED\u001b[31m\rline2"}`
	res := gateExecResult{stdout: raw + "\n"}
	_, gro, _ := deriveGateStatusAndVerdict(res, nil, GateOnFailBlock)
	if strings.Contains(gro.FixHint, "\x1b") {
		t.Errorf("fix_hint ESC not stripped: %q", gro.FixHint)
	}
	if strings.Contains(gro.FixHint, "\r") {
		t.Errorf("fix_hint CR not stripped: %q", gro.FixHint)
	}
	if strings.Contains(gro.FixHint, "\n") {
		t.Errorf("fix_hint newline not folded: %q", gro.FixHint)
	}
}

// errSentinel is a tiny error type for the derive test (avoid pulling
// errors.New into the test imports just for one call).
type errSentinel string

func (e errSentinel) Error() string { return string(e) }

// ---------------------------------------------------------------------------
// allowlistEnv — security contract #1 (PRD R6)
// ---------------------------------------------------------------------------

func TestAllowlistEnv_DropsSecrets(t *testing.T) {
	t.Parallel()
	// Build the allowlist purely from os.Environ so the test does not
	// mutate the process environment. The test asserts that EVERY key
	// in the result is in the allow set; the server's actual secret
	// values are irrelevant because the names are filtered.
	allow := map[string]bool{"PATH": true, "HOME": true, "LANG": true, "LC_ALL": true, "TZ": true}
	for _, kv := range allowlistEnv() {
		key := kv
		if i := strings.IndexByte(kv, '='); i > 0 {
			key = kv[:i]
		}
		if !allow[key] {
			t.Errorf("allowlistEnv leaked key %q (kv=%q)", key, kv)
		}
	}
	// Sanity: PATH is essentially always set on POSIX dev/CI hosts, so
	// the allow list should produce at least one entry. (This is a
	// smoke check, not a strict assertion, to stay portable.)
	if len(allowlistEnv()) == 0 {
		t.Errorf("allowlistEnv returned empty; expected at least PATH")
	}
}
