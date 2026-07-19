package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// cmd_step_test.go — CLI contract tests for `multica step context`: the
// human-readable rendering prints node instructions + upstream exit fields +
// the exit-fields schema, and --output json passes the API response through.
// Stdout is captured via the shared captureStdout helper (cmd_skill_test.go).

func newStepContextTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "context"}
	cmd.Flags().String("task", "", "")
	cmd.Flags().String("output", "text", "")
	cmd.Flags().String("profile", "", "")
	return cmd
}

const stepContextFixture = `{
  "node_key": "gate",
  "node_type": "agent",
  "node_name": "Baseline Gate",
  "role": "evaluator",
  "attempt": 1,
  "step_status": "active",
  "instructions": "Judge the upstream implementation against the baseline.",
  "exit_fields_schema": {"fields": [
    {"name": "verdict_score", "type": "number", "required": true, "description": "0..1"},
    {"name": "notes", "type": "string"}
  ]},
  "upstream_node_key": "work",
  "upstream_exit_fields": {"branch": "feat/x", "pr_url": "https://example/pr/1"}
}`

func stepContextServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tasks/"+cliTestTaskID+"/step-context" || r.Method != http.MethodGet {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(stepContextFixture))
	}))
}

func TestStepContextHumanOutput(t *testing.T) {
	srv := stepContextServer(t)
	defer srv.Close()

	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
	t.Setenv("MULTICA_TOKEN", "mat_testtoken")
	t.Setenv("MULTICA_TASK_ID", cliTestTaskID)

	cmd := newStepContextTestCmd()
	out, err := captureStdout(t, func() error {
		return runStepContext(cmd, nil)
	})
	if err != nil {
		t.Fatalf("runStepContext: %v", err)
	}

	for _, want := range []string{
		"Node: Baseline Gate (gate) — role evaluator, attempt 1, status active",
		"Judge the upstream implementation against the baseline.",
		"Upstream exit_fields (work):",
		`"branch": "feat/x"`,
		"Exit fields schema:",
		"verdict_score (number, required): 0..1",
		"notes (string)",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestStepContextJSONOutput(t *testing.T) {
	srv := stepContextServer(t)
	defer srv.Close()

	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
	t.Setenv("MULTICA_TOKEN", "mat_testtoken")
	t.Setenv("MULTICA_TASK_ID", cliTestTaskID)

	cmd := newStepContextTestCmd()
	_ = cmd.Flags().Set("output", "json")
	out, err := captureStdout(t, func() error {
		return runStepContext(cmd, nil)
	})
	if err != nil {
		t.Fatalf("runStepContext: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("--output json did not print valid JSON: %v\n%s", err, out)
	}
	if decoded["node_key"] != "gate" || decoded["role"] != "evaluator" {
		t.Fatalf("decoded = %v", decoded)
	}
}
