package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// cmd_verdict_test.go — CLI contract tests for `multica verdict
// create/get`: result/confidence validation is client-side, confidence maps
// to the numeric API scale, and a server 403 (executor-role token, verdict
// actor model) surfaces as a classifiable auth error.

func newVerdictTestCmd(use string) *cobra.Command {
	cmd := &cobra.Command{Use: use}
	cmd.Flags().String("task", "", "")
	cmd.Flags().String("result", "", "")
	cmd.Flags().String("root-cause", "", "")
	cmd.Flags().String("confidence", "", "")
	cmd.Flags().String("exit-fields", "", "")
	cmd.Flags().String("output", "json", "")
	cmd.Flags().String("profile", "", "")
	return cmd
}

func TestVerdictCreateHappyPath(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "v-1", "result": "pass", "verdict_by": "agent"})
	}))
	defer srv.Close()

	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
	t.Setenv("MULTICA_TOKEN", "mat_testtoken")
	t.Setenv("MULTICA_TASK_ID", cliTestTaskID)

	cmd := newVerdictTestCmd("create")
	for _, kv := range [][2]string{
		{"result", "pass"},
		{"root-cause", ""},
		{"confidence", "high"},
		{"exit-fields", `{"verdict_score":0.95}`},
	} {
		if err := cmd.Flags().Set(kv[0], kv[1]); err != nil {
			t.Fatal(err)
		}
	}

	if err := runVerdictCreate(cmd, nil); err != nil {
		t.Fatalf("runVerdictCreate: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/tasks/"+cliTestTaskID+"/verdict" {
		t.Fatalf("%s %s, want POST /api/tasks/<id>/verdict", gotMethod, gotPath)
	}
	if gotBody["result"] != "pass" {
		t.Fatalf("result = %v", gotBody["result"])
	}
	if gotBody["confidence"] != 0.9 {
		t.Fatalf("confidence = %v, want 0.9 (high mapped to the numeric scale)", gotBody["confidence"])
	}
	exitFields, _ := gotBody["exit_fields"].(map[string]any)
	if exitFields["verdict_score"] != 0.95 {
		t.Fatalf("exit_fields = %v", gotBody["exit_fields"])
	}
	if _, present := gotBody["root_cause"]; present {
		t.Fatalf("empty --root-cause must be omitted, body = %v", gotBody)
	}
}

// TestVerdictCreateExecutorForbidden: the server enforces the verdict actor
// model (design.md §4.3) with a 403; the CLI must surface it as an
// auth-classified error (exit code 3 via cli.ExitCodeFor), not swallow it.
func TestVerdictCreateExecutorForbidden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "verdict writes require an evaluator-role step"})
	}))
	defer srv.Close()

	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
	t.Setenv("MULTICA_TOKEN", "mat_testtoken")
	t.Setenv("MULTICA_TASK_ID", cliTestTaskID)

	cmd := newVerdictTestCmd("create")
	_ = cmd.Flags().Set("result", "pass")

	err := runVerdictCreate(cmd, nil)
	if err == nil {
		t.Fatalf("expected the 403 to surface as an error")
	}
	var httpErr *cli.HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusForbidden {
		t.Fatalf("err = %v, want a 403 HTTPError", err)
	}
	if code := cli.ExitCodeFor(err); code != cli.ExitAuth {
		t.Fatalf("exit code = %d, want %d (auth)", code, cli.ExitAuth)
	}
}

func TestVerdictCreateValidation(t *testing.T) {
	t.Setenv("MULTICA_SERVER_URL", "http://127.0.0.1:0")
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
	t.Setenv("MULTICA_TOKEN", "mat_testtoken")
	t.Setenv("MULTICA_TASK_ID", cliTestTaskID)

	cases := []struct {
		name    string
		flags   [][2]string
		wantErr string
	}{
		{"missing result", nil, "--result is required"},
		{"invalid result", [][2]string{{"result", "approved"}}, "invalid --result"},
		{"invalid confidence word", [][2]string{{"result", "pass"}, {"confidence", "sure"}}, "invalid --confidence"},
		{"confidence out of range", [][2]string{{"result", "pass"}, {"confidence", "1.5"}}, "invalid --confidence"},
		{"invalid exit-fields json", [][2]string{{"result", "pass"}, {"exit-fields", "{nope"}}, "invalid --exit-fields JSON"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := newVerdictTestCmd("create")
			for _, kv := range tc.flags {
				if err := cmd.Flags().Set(kv[0], kv[1]); err != nil {
					t.Fatal(err)
				}
			}
			err := runVerdictCreate(cmd, nil)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want substring %q", err, tc.wantErr)
			}
		})
	}
}

func TestVerdictConfidenceLevels(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want float64
	}{
		{"high", 0.9},
		{"medium", 0.6},
		{"low", 0.3},
		{"0.42", 0.42},
	} {
		cmd := newVerdictTestCmd("create")
		_ = cmd.Flags().Set("confidence", tc.in)
		got, err := parseVerdictConfidence(cmd)
		if err != nil || got == nil || *got != tc.want {
			t.Fatalf("parseVerdictConfidence(%q) = %v, %v; want %v", tc.in, got, err, tc.want)
		}
	}
}

func TestVerdictGetHappyPath(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "v-1", "result": "pass", "verdict_by": "system"})
	}))
	defer srv.Close()

	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
	t.Setenv("MULTICA_TOKEN", "mat_testtoken")
	t.Setenv("MULTICA_TASK_ID", cliTestTaskID)

	cmd := newVerdictTestCmd("get")
	if err := runVerdictGet(cmd, nil); err != nil {
		t.Fatalf("runVerdictGet: %v", err)
	}
	if gotMethod != http.MethodGet || gotPath != "/api/tasks/"+cliTestTaskID+"/verdict" {
		t.Fatalf("%s %s, want GET /api/tasks/<id>/verdict", gotMethod, gotPath)
	}
}

// TestVerdictGetRejectsNonJSONOutput: the verdict commands are json-only — a
// stray --output value is a usage error before any HTTP call, not a silently
// ignored flag.
func TestVerdictGetRejectsNonJSONOutput(t *testing.T) {
	t.Setenv("MULTICA_SERVER_URL", "http://127.0.0.1:0")
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
	t.Setenv("MULTICA_TOKEN", "mat_testtoken")
	t.Setenv("MULTICA_TASK_ID", cliTestTaskID)

	cmd := newVerdictTestCmd("get")
	_ = cmd.Flags().Set("output", "table")
	err := runVerdictGet(cmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--output must be json") {
		t.Fatalf("err = %v, want the --output json rejection", err)
	}
}