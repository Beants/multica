package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// cmd_workflow.go — `multica workflow` (workflow fork command, R8): operator
// surface for the workflow engine. P0 ships `workflow seed`, which installs
// the standard/bugfix seed templates into the workspace via POST
// /api/workflow-templates/seed. Seeding is an EXPLICIT command (never an
// auto-seed on a read path): a GET that silently writes templates would
// surprise workspaces that never asked for them.
//
// Root-command registration happens in this file's init (not main.go) so the
// fork adds zero lines to upstream files.

var workflowCmd = &cobra.Command{
	Use:     "workflow",
	Short:   "Manage workflow templates and runs",
	GroupID: groupCore,
}

var workflowSeedCmd = &cobra.Command{
	Use:   "seed",
	Short: "Install the standard and bugfix seed templates into the workspace",
	Long: `Install the two P0 seed workflow templates (standard: 9-node requirement
chain; bugfix: 6-node fix chain) into the workspace, creating and publishing
them bound to workspace agents.

Seeding is idempotent: a template key that already exists in the workspace is
skipped, never duplicated. Agent bindings default to placeholder names
(workflow-planner / workflow-implementer / workflow-gate-runner /
workflow-reviewer) — create agents with those names first, or override with
the --*-agent flags (name or UUID). Gate/review agents must differ from the
executor agents (produce/review separation).`,
	RunE: runWorkflowSeed,
}

func init() {
	rootCmd.AddCommand(workflowCmd)
	workflowCmd.AddCommand(workflowSeedCmd)

	workflowSeedCmd.Flags().String("planner-agent", "", "Agent (name or UUID) for the plan stages (default "+defaultSeedPlannerName+")")
	workflowSeedCmd.Flags().String("implementer-agent", "", "Agent (name or UUID) for the implement stage (default "+defaultSeedImplementerName+")")
	workflowSeedCmd.Flags().String("gate-agent", "", "Agent (name or UUID) for the gate stages (default "+defaultSeedGateName+")")
	workflowSeedCmd.Flags().String("review-agent", "", "Agent (name or UUID) for the review stage (default "+defaultSeedReviewName+")")
	workflowSeedCmd.Flags().String("output", "text", "Output format: text or json")
}

// Placeholder names mirrored from the server's workflow seed defaults
// (server/internal/workflow/seed.go) for flag help text only — the server
// applies its own defaults when a flag is empty.
const (
	defaultSeedPlannerName     = "workflow-planner"
	defaultSeedImplementerName = "workflow-implementer"
	defaultSeedGateName        = "workflow-gate-runner"
	defaultSeedReviewName      = "workflow-reviewer"
)

// workflowSeedEntry mirrors the server's workflow.SeedResult wire shape.
type workflowSeedEntry struct {
	Key        string `json:"key"`
	TemplateID string `json:"template_id"`
	Version    int32  `json:"version"`
	Seeded     bool   `json:"seeded"`
}

type workflowSeedResponse struct {
	Templates []workflowSeedEntry `json:"templates"`
}

func runWorkflowSeed(cmd *cobra.Command, _ []string) error {
	output, _ := cmd.Flags().GetString("output")
	if output != "text" && output != "json" {
		return fmt.Errorf("--output must be text or json, got %q", output)
	}

	body := map[string]any{}
	for flag, key := range map[string]string{
		"planner-agent":     "planner_agent",
		"implementer-agent": "implementer_agent",
		"gate-agent":        "gate_agent",
		"review-agent":      "review_agent",
	} {
		if v, _ := cmd.Flags().GetString(flag); v != "" {
			body[key] = v
		}
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var resp workflowSeedResponse
	if err := client.PostJSON(ctx, "/api/workflow-templates/seed", body, &resp); err != nil {
		return fmt.Errorf("seed workflow templates: %w", err)
	}

	if output == "json" {
		return cli.PrintJSON(os.Stdout, resp)
	}
	printSeedResult(os.Stdout, resp.Templates)
	return nil
}

func printSeedResult(w io.Writer, templates []workflowSeedEntry) {
	for _, t := range templates {
		if t.Seeded {
			fmt.Fprintf(w, "seeded   %s (template %s, v%d)\n", t.Key, t.TemplateID, t.Version)
		} else {
			fmt.Fprintf(w, "skipped  %s (already exists in workspace)\n", t.Key)
		}
	}
}
