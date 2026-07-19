package main

import (
	"context"
	"fmt"
	"net/url"
	"os"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// cmd_submission.go — `multica submission` (workflow fork command, R4): an
// agent records its step's work product (status + exit_fields + artifacts)
// via POST /api/tasks/{id}/submission. The command only WRITES submissions;
// verdicts live behind `multica verdict` (design.md §2 — the verdict actor
// model keeps the two write surfaces separate).
//
// Root-command registration happens in this file's init (not main.go) so the
// fork adds zero lines to upstream files; cmd_*.go inits run before
// main.go's initHelp, so help templates/groups still apply.

var submissionCmd = &cobra.Command{
	Use:     "submission",
	Short:   "Submit workflow step results",
	GroupID: groupCore,
}

var submissionCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Record a submission for the workflow step bound to a task",
	Long: `Record the agent's work product for the workflow step bound to a task.

Status vocabulary (harness four-state):
  DONE                work finished, required exit fields complete
  DONE_WITH_CONCERNS  work finished with reservations — record them via --gaps
  BLOCKED             cannot proceed — say why via --summary
  NEEDS_CONTEXT       missing information — say what via --summary

Exit fields are validated against the node's frozen schema: missing required
fields are rejected with a structured error; unknown fields pass through.`,
	RunE: runSubmissionCreate,
}

func init() {
	rootCmd.AddCommand(submissionCmd)
	submissionCmd.AddCommand(submissionCreateCmd)

	submissionCreateCmd.Flags().String("task", "", "Agent task ID (defaults to MULTICA_TASK_ID inside a daemon task)")
	submissionCreateCmd.Flags().String("status", "", "Submission status: DONE, DONE_WITH_CONCERNS, BLOCKED, NEEDS_CONTEXT (required)")
	submissionCreateCmd.Flags().String("exit-fields", "", `Exit fields as a JSON object, e.g. '{"pr_url":"https://…"}'`)
	submissionCreateCmd.Flags().String("artifacts", "", "Artifacts as JSON — durable references only (PR URL, branch, attachment ID); local/workdir paths are rejected")
	submissionCreateCmd.Flags().String("gaps", "", "Gaps or concerns as JSON (recorded with DONE_WITH_CONCERNS)")
	submissionCreateCmd.Flags().String("summary", "", "Free-text summary of the outcome (stored as raw_summary)")
	submissionCreateCmd.Flags().String("idempotency-key", "", "Idempotency key: a retry with the same key returns the original submission instead of a conflict")
	submissionCreateCmd.Flags().String("output", "json", "Output format: json")
}

func runSubmissionCreate(cmd *cobra.Command, _ []string) error {
	if err := requireJSONOutput(cmd); err != nil {
		return err
	}
	taskID, err := resolveStepTaskID(cmd)
	if err != nil {
		return err
	}
	status, _ := cmd.Flags().GetString("status")
	switch status {
	case "DONE", "DONE_WITH_CONCERNS", "BLOCKED", "NEEDS_CONTEXT":
	case "":
		return fmt.Errorf("--status is required (DONE, DONE_WITH_CONCERNS, BLOCKED, NEEDS_CONTEXT)")
	default:
		return fmt.Errorf("invalid --status %q; valid values: DONE, DONE_WITH_CONCERNS, BLOCKED, NEEDS_CONTEXT", status)
	}

	body := map[string]any{"status": status}
	if err := setJSONFlag(cmd, body, "exit-fields", "exit_fields", true); err != nil {
		return err
	}
	if err := setJSONFlag(cmd, body, "artifacts", "artifacts", false); err != nil {
		return err
	}
	if err := setJSONFlag(cmd, body, "gaps", "gaps", false); err != nil {
		return err
	}
	if summary, _ := cmd.Flags().GetString("summary"); summary != "" {
		body["raw_summary"] = summary
	}
	if key, _ := cmd.Flags().GetString("idempotency-key"); key != "" {
		body["idempotency_key"] = key
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var result map[string]any
	path := "/api/tasks/" + url.PathEscape(taskID) + "/submission"
	if err := client.PostJSON(ctx, path, body, &result); err != nil {
		return fmt.Errorf("create submission: %w", err)
	}
	return cli.PrintJSON(os.Stdout, result)
}
