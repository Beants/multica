package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// cmd_submission_test.go — CLI contract tests for `multica submission
// create`: flag validation happens client-side before any HTTP call, and the
// happy path posts the exact API shape to /api/tasks/{id}/submission.

const cliTestTaskID = "11111111-1111-1111-1111-111111111111"

// newSubmissionCreateTestCmd builds a throwaway command carrying the same
// flags as submissionCreateCmd (mirrors cmd_agent_test.go conventions).
func newSubmissionCreateTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "create"}
	cmd.Flags().String("task", "", "")
	cmd.Flags().String("status", "", "")
	cmd.Flags().String("exit-fields", "", "")
	cmd.Flags().String("artifacts", "", "")
	cmd.Flags().String("gaps", "", "")
	cmd.Flags().String("summary", "", "")
	cmd.Flags().String("idempotency-key", "", "")
	cmd.Flags().String("output", "json", "")
	cmd.Flags().String("profile", "", "")
	return cmd
}

func TestSubmissionCreateHappyPath(t *testing.T) {
	var gotMethod, gotPath, gotAuth string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotAuth = r.Method, r.URL.Path, r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "sub-1", "status": "DONE", "created": true})
	}))
	defer srv.Close()

	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
	t.Setenv("MULTICA_TOKEN", "mat_testtoken")
	t.Setenv("MULTICA_TASK_ID", "")

	cmd := newSubmissionCreateTestCmd()
	for _, kv := range [][2]string{
		{"task", cliTestTaskID},
		{"status", "DONE_WITH_CONCERNS"},
		{"exit-fields", `{"pr_url":"https://example/pr/1"}`},
		{"gaps", `[{"kind":"coverage"}]`},
		{"artifacts", `{"pr":"https://example/pr/1"}`},
		{"summary", "done with reservations"},
		{"idempotency-key", "k-1"},
	} {
		if err := cmd.Flags().Set(kv[0], kv[1]); err != nil {
			t.Fatal(err)
		}
	}

	if err := runSubmissionCreate(cmd, nil); err != nil {
		t.Fatalf("runSubmissionCreate: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("method = %s, want POST", gotMethod)
	}
	if gotPath != "/api/tasks/"+cliTestTaskID+"/submission" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotAuth != "Bearer mat_testtoken" {
		t.Fatalf("authorization = %q, want the mat_ token", gotAuth)
	}
	if gotBody["status"] != "DONE_WITH_CONCERNS" {
		t.Fatalf("status = %v", gotBody["status"])
	}
	exitFields, _ := gotBody["exit_fields"].(map[string]any)
	if exitFields["pr_url"] != "https://example/pr/1" {
		t.Fatalf("exit_fields = %v", gotBody["exit_fields"])
	}
	if gotBody["raw_summary"] != "done with reservations" || gotBody["idempotency_key"] != "k-1" {
		t.Fatalf("body = %v", gotBody)
	}
	if _, ok := gotBody["gaps"].([]any); !ok {
		t.Fatalf("gaps = %v, want a JSON array passed through", gotBody["gaps"])
	}
}

func TestSubmissionCreateTaskDefaultsToEnv(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "sub-1"})
	}))
	defer srv.Close()

	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
	t.Setenv("MULTICA_TOKEN", "mat_testtoken")
	t.Setenv("MULTICA_TASK_ID", cliTestTaskID)

	cmd := newSubmissionCreateTestCmd()
	_ = cmd.Flags().Set("status", "DONE")

	if err := runSubmissionCreate(cmd, nil); err != nil {
		t.Fatalf("runSubmissionCreate: %v", err)
	}
	if gotPath != "/api/tasks/"+cliTestTaskID+"/submission" {
		t.Fatalf("path = %q, want MULTICA_TASK_ID fallback", gotPath)
	}
}

// TestSubmissionCreateValidation checks every client-side rejection fires
// BEFORE any HTTP call (the server URL is unreachable on purpose).
func TestSubmissionCreateValidation(t *testing.T) {
	t.Setenv("MULTICA_SERVER_URL", "http://127.0.0.1:0")
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
	t.Setenv("MULTICA_TOKEN", "mat_testtoken")
	t.Setenv("MULTICA_TASK_ID", "")

	cases := []struct {
		name    string
		flags   [][2]string
		wantErr string
	}{
		{"missing task", [][2]string{{"status", "DONE"}}, "--task is required"},
		{"non-uuid task", [][2]string{{"task", "abc"}, {"status", "DONE"}}, "canonical task UUID"},
		{"missing status", [][2]string{{"task", cliTestTaskID}}, "--status is required"},
		{"invalid status", [][2]string{{"task", cliTestTaskID}, {"status", "done"}}, "invalid --status"},
		{"invalid exit-fields json", [][2]string{{"task", cliTestTaskID}, {"status", "DONE"}, {"exit-fields", "{nope"}}, "invalid --exit-fields JSON"},
		{"exit-fields not object", [][2]string{{"task", cliTestTaskID}, {"status", "DONE"}, {"exit-fields", `[1,2]`}}, "must be a JSON object"},
		{"invalid artifacts json", [][2]string{{"task", cliTestTaskID}, {"status", "DONE"}, {"artifacts", "{nope"}}, "invalid --artifacts JSON"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := newSubmissionCreateTestCmd()
			for _, kv := range tc.flags {
				if err := cmd.Flags().Set(kv[0], kv[1]); err != nil {
					t.Fatal(err)
				}
			}
			err := runSubmissionCreate(cmd, nil)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want substring %q", err, tc.wantErr)
			}
		})
	}
}
