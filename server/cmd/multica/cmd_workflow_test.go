package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// cmd_workflow_test.go — CLI contract tests for `multica workflow seed`:
// the command POSTs the selector overrides to /api/workflow-templates/seed
// and renders per-template seeded/skipped outcomes.

func newWorkflowSeedTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "seed"}
	cmd.Flags().String("planner-agent", "", "")
	cmd.Flags().String("implementer-agent", "", "")
	cmd.Flags().String("gate-agent", "", "")
	cmd.Flags().String("review-agent", "", "")
	cmd.Flags().String("output", "text", "")
	cmd.Flags().String("profile", "", "")
	return cmd
}

const workflowSeedFixture = `{
  "templates": [
    {"key": "standard", "template_id": "11111111-1111-1111-1111-111111111111", "version": 1, "seeded": true},
    {"key": "bugfix", "seeded": false}
  ]
}`

// workflowSeedServer asserts the request shape and answers the fixture.
func workflowSeedServer(t *testing.T, wantBody map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/workflow-templates/seed" || r.Method != http.MethodPost {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Errorf("request body not JSON: %v", err)
			}
		}
		for key, want := range wantBody {
			if body[key] != want {
				t.Errorf("body[%q] = %v, want %q", key, body[key], want)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(workflowSeedFixture))
	}))
}

func TestWorkflowSeedTextOutput(t *testing.T) {
	srv := workflowSeedServer(t, map[string]string{
		"planner_agent": "my-planner",
		"gate_agent":    "my-gate",
	})
	defer srv.Close()

	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
	t.Setenv("MULTICA_TOKEN", "pat_testtoken")

	cmd := newWorkflowSeedTestCmd()
	_ = cmd.Flags().Set("planner-agent", "my-planner")
	_ = cmd.Flags().Set("gate-agent", "my-gate")
	out, err := captureStdout(t, func() error {
		return runWorkflowSeed(cmd, nil)
	})
	if err != nil {
		t.Fatalf("runWorkflowSeed: %v", err)
	}
	for _, want := range []string{
		"seeded   standard (template 11111111-1111-1111-1111-111111111111, v1)",
		"skipped  bugfix (already exists in workspace)",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestWorkflowSeedJSONOutput(t *testing.T) {
	srv := workflowSeedServer(t, map[string]string{})
	defer srv.Close()

	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
	t.Setenv("MULTICA_TOKEN", "pat_testtoken")

	cmd := newWorkflowSeedTestCmd()
	_ = cmd.Flags().Set("output", "json")
	out, err := captureStdout(t, func() error {
		return runWorkflowSeed(cmd, nil)
	})
	if err != nil {
		t.Fatalf("runWorkflowSeed: %v", err)
	}
	var decoded struct {
		Templates []struct {
			Key    string `json:"key"`
			Seeded bool   `json:"seeded"`
		} `json:"templates"`
	}
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("--output json did not print valid JSON: %v\n%s", err, out)
	}
	if len(decoded.Templates) != 2 || !decoded.Templates[0].Seeded || decoded.Templates[1].Seeded {
		t.Fatalf("decoded = %+v", decoded)
	}
}

func TestWorkflowSeedRejectsBadOutputFlag(t *testing.T) {
	cmd := newWorkflowSeedTestCmd()
	_ = cmd.Flags().Set("output", "yaml")
	if err := runWorkflowSeed(cmd, nil); err == nil || !strings.Contains(err.Error(), "--output") {
		t.Fatalf("err = %v, want --output validation error", err)
	}
}

// TestWorkflowSeedFlagOffCleanError: a flag-off deployment 404s every
// workflow route (AC6 — indistinguishable from never registered). The CLI
// must surface that as a clean, branchable error (HTTPError 404 → exit code
// ExitNotFound), not a crash or an opaque failure.
func TestWorkflowSeedFlagOffCleanError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not found"}`))
	}))
	defer srv.Close()

	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
	t.Setenv("MULTICA_TOKEN", "pat_testtoken")

	err := runWorkflowSeed(newWorkflowSeedTestCmd(), nil)
	if err == nil {
		t.Fatalf("expected the 404 to surface as an error")
	}
	var httpErr *cli.HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusNotFound {
		t.Fatalf("err = %v, want a 404 HTTPError", err)
	}
	if code := cli.ExitCodeFor(err); code != cli.ExitNotFound {
		t.Fatalf("exit code = %d, want %d (not-found)", code, cli.ExitNotFound)
	}
}
