package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// cmd_verdict.go — `multica verdict` (workflow fork command, R3 verdict
// actor model): an EVALUATOR-role agent writes the verdict for its own step
// via POST /api/tasks/{id}/verdict; any step token can read the verdict back
// via GET. Executor-role tokens get a 403 on write — their steps are judged
// by the system-derived verdict instead (design.md §4.3).
//
// Root-command registration happens in this file's init (not main.go) so the
// fork adds zero lines to upstream files.

var verdictCmd = &cobra.Command{
	Use:     "verdict",
	Short:   "Judge workflow steps (evaluator role)",
	GroupID: groupCore,
}

var verdictCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Write the verdict for the workflow step bound to a task",
	Long: `Write the evaluator's verdict for the workflow step bound to a task.

Only evaluator-role steps accept verdict writes (executor tokens get 403).
When the step has no submission yet, the server auto-creates a minimal one in
the same transaction — through the same exit-fields validation, so a node
with required exit fields rejects a verdict that omits them.`,
	RunE: runVerdictCreate,
}

var verdictGetCmd = &cobra.Command{
	Use:   "get",
	Short: "Read the verdict attached to the step bound to a task",
	RunE:  runVerdictGet,
}

func init() {
	rootCmd.AddCommand(verdictCmd)
	verdictCmd.AddCommand(verdictCreateCmd)
	verdictCmd.AddCommand(verdictGetCmd)

	verdictCreateCmd.Flags().String("task", "", "Agent task ID (defaults to MULTICA_TASK_ID inside a daemon task)")
	verdictCreateCmd.Flags().String("result", "", "Verdict result: pass, fail, blocked (required)")
	verdictCreateCmd.Flags().String("root-cause", "", "Root cause of a fail/blocked verdict")
	verdictCreateCmd.Flags().String("confidence", "", "Confidence: high, medium, low, or a number in (0,1]")
	verdictCreateCmd.Flags().String("exit-fields", "", `Exit fields as a JSON object (required when the node's schema has required fields and the step has no submission yet)`)
	verdictCreateCmd.Flags().String("output", "json", "Output format: json")

	verdictGetCmd.Flags().String("task", "", "Agent task ID (defaults to MULTICA_TASK_ID inside a daemon task)")
	verdictGetCmd.Flags().String("output", "json", "Output format: json")
}

func runVerdictCreate(cmd *cobra.Command, _ []string) error {
	if err := requireJSONOutput(cmd); err != nil {
		return err
	}
	taskID, err := resolveStepTaskID(cmd)
	if err != nil {
		return err
	}
	result, _ := cmd.Flags().GetString("result")
	switch result {
	case "pass", "fail", "blocked":
	case "":
		return fmt.Errorf("--result is required (pass, fail, blocked)")
	default:
		return fmt.Errorf("invalid --result %q; valid values: pass, fail, blocked", result)
	}
	confidence, err := parseVerdictConfidence(cmd)
	if err != nil {
		return err
	}

	body := map[string]any{"result": result}
	if rootCause, _ := cmd.Flags().GetString("root-cause"); rootCause != "" {
		body["root_cause"] = rootCause
	}
	if confidence != nil {
		body["confidence"] = *confidence
	}
	if err := setJSONFlag(cmd, body, "exit-fields", "exit_fields", true); err != nil {
		return err
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var verdict map[string]any
	path := "/api/tasks/" + url.PathEscape(taskID) + "/verdict"
	if err := client.PostJSON(ctx, path, body, &verdict); err != nil {
		return fmt.Errorf("create verdict: %w", err)
	}
	return cli.PrintJSON(os.Stdout, verdict)
}

func runVerdictGet(cmd *cobra.Command, _ []string) error {
	if err := requireJSONOutput(cmd); err != nil {
		return err
	}
	taskID, err := resolveStepTaskID(cmd)
	if err != nil {
		return err
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var verdict map[string]any
	path := "/api/tasks/" + url.PathEscape(taskID) + "/verdict"
	if err := client.GetJSON(ctx, path, &verdict); err != nil {
		return fmt.Errorf("get verdict: %w", err)
	}
	return cli.PrintJSON(os.Stdout, verdict)
}

// parseVerdictConfidence maps the named levels to the numeric scale the API
// stores (high=0.9, medium=0.6, low=0.3); a raw float in (0,1] passes
// through for scripts that need finer granularity.
func parseVerdictConfidence(cmd *cobra.Command) (*float64, error) {
	raw, _ := cmd.Flags().GetString("confidence")
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return nil, nil
	case "high":
		v := 0.9
		return &v, nil
	case "medium":
		v := 0.6
		return &v, nil
	case "low":
		v := 0.3
		return &v, nil
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil || f <= 0 || f > 1 {
		return nil, fmt.Errorf("invalid --confidence %q; valid values: high, medium, low, or a number in (0,1]", raw)
	}
	return &f, nil
}
