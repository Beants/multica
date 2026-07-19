package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// cmd_step.go — `multica step` (workflow fork command, R10): read-side
// inspection for the step bound to an agent task. `step context` is the
// full-fidelity sibling of the handoff note injected at dispatch (the note
// itself points agents here for anything it truncated, design.md §4.2).
//
// This file also hosts the helpers shared by the three step-bound commands
// (submission/verdict/step): --task resolution and JSON flag parsing.
//
// Root-command registration happens in this file's init (not main.go) so the
// fork adds zero lines to upstream files.

var stepCmd = &cobra.Command{
	Use:     "step",
	Short:   "Inspect workflow steps",
	GroupID: groupCore,
}

var stepContextCmd = &cobra.Command{
	Use:   "context",
	Short: "Print the full node context for the step bound to a task",
	Long: `Print the node context for the workflow step bound to a task: the node's
instructions, the immediate upstream node's exit fields, and this node's
exit-fields schema — everything the truncated handoff note points at.`,
	RunE: runStepContext,
}

func init() {
	rootCmd.AddCommand(stepCmd)
	stepCmd.AddCommand(stepContextCmd)

	stepContextCmd.Flags().String("task", "", "Agent task ID (defaults to MULTICA_TASK_ID inside a daemon task)")
	stepContextCmd.Flags().String("output", "text", "Output format: text or json")
}

func runStepContext(cmd *cobra.Command, _ []string) error {
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

	var sc map[string]any
	path := "/api/tasks/" + url.PathEscape(taskID) + "/step-context"
	if err := client.GetJSON(ctx, path, &sc); err != nil {
		return fmt.Errorf("get step context: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, sc)
	}
	printStepContext(os.Stdout, sc)
	return nil
}

// printStepContext renders the step context for a human (or an agent reading
// plain text): node identity, instructions, upstream exit fields, and the
// exit-fields schema with required fields marked.
func printStepContext(w io.Writer, sc map[string]any) {
	fmt.Fprintf(w, "Node: %s (%s) — role %s, attempt %v, status %s\n",
		strVal(sc, "node_name"), strVal(sc, "node_key"),
		strVal(sc, "role"), sc["attempt"], strVal(sc, "step_status"))

	if instructions := strVal(sc, "instructions"); instructions != "" {
		fmt.Fprintf(w, "\nInstructions:\n%s\n", instructions)
	}

	if upstream := strVal(sc, "upstream_node_key"); upstream != "" {
		fmt.Fprintf(w, "\nUpstream exit_fields (%s):\n", upstream)
		writeJSONBlock(w, sc["upstream_exit_fields"])
	}

	schema, _ := sc["exit_fields_schema"].(map[string]any)
	fields, _ := schema["fields"].([]any)
	if len(fields) == 0 {
		return
	}
	fmt.Fprintf(w, "\nExit fields schema:\n")
	for _, raw := range fields {
		field, _ := raw.(map[string]any)
		typ := strVal(field, "type")
		if typ == "" {
			typ = "any"
		}
		required := ""
		if req, _ := field["required"].(bool); req {
			required = ", required"
		}
		line := fmt.Sprintf("  - %s (%s%s)", strVal(field, "name"), typ, required)
		if desc := strVal(field, "description"); desc != "" {
			line += ": " + desc
		}
		fmt.Fprintln(w, line)
	}
}

// writeJSONBlock prints a decoded JSON value as an indented block.
func writeJSONBlock(w io.Writer, v any) {
	if v == nil {
		fmt.Fprintln(w, "  (none)")
		return
	}
	raw, err := json.MarshalIndent(v, "  ", "  ")
	if err != nil {
		fmt.Fprintf(w, "  %v\n", v)
		return
	}
	fmt.Fprintf(w, "  %s\n", raw)
}

// ---------------------------------------------------------------------------
// Shared helpers for the step-bound commands (submission/verdict/step)
// ---------------------------------------------------------------------------

// requireJSONOutput enforces the json-only output contract of the
// submission/verdict commands: another format is a usage error, not a
// silently ignored flag (the step-context command supports text|json and
// branches on the flag instead).
func requireJSONOutput(cmd *cobra.Command) error {
	output, _ := cmd.Flags().GetString("output")
	if output != "json" {
		return fmt.Errorf("--output must be json (the only format this command supports), got %q", output)
	}
	return nil
}

// resolveStepTaskID resolves the --task flag, defaulting to the daemon-
// stamped MULTICA_TASK_ID (the common case: the agent runs inside its own
// task). The API requires the URL task id to equal the token's bound task,
// so only the canonical UUID is accepted — no short-prefix resolution.
func resolveStepTaskID(cmd *cobra.Command) (string, error) {
	taskID, _ := cmd.Flags().GetString("task")
	if taskID == "" {
		taskID = strings.TrimSpace(os.Getenv("MULTICA_TASK_ID"))
	}
	if taskID == "" {
		return "", fmt.Errorf("--task is required (or run inside a daemon task with MULTICA_TASK_ID set)")
	}
	if !uuidRegexp.MatchString(taskID) {
		return "", fmt.Errorf("--task must be a canonical task UUID, got %q", taskID)
	}
	return taskID, nil
}

// setJSONFlag parses a JSON-valued flag into the request body. objectOnly
// restricts the value to a JSON object (exit_fields); the other blobs accept
// any JSON shape. Client-side parsing fails fast naming the flag — a
// malformed blob would otherwise round-trip to a server 400.
func setJSONFlag(cmd *cobra.Command, body map[string]any, flagName, bodyKey string, objectOnly bool) error {
	raw, _ := cmd.Flags().GetString(flagName)
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return fmt.Errorf("invalid --%s JSON: %w", flagName, err)
	}
	if objectOnly {
		if _, ok := v.(map[string]any); !ok {
			return fmt.Errorf("--%s must be a JSON object", flagName)
		}
	}
	body[bodyKey] = v
	return nil
}
