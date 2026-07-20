// cmd_workflow_import.go — `multica workflow import-yaml` subcommand (PRD
// P1-8 / R5). Reads a harness pipeline YAML, converts it to a draft
// WorkflowTemplate graph, and writes it via TemplateService.CreateTemplate.
//
// Unlike the rest of the `multica workflow` surface (which is HTTP-driven
// via /api/workflow-templates/*), import-yaml talks to the database
// directly. Rationale: the import path is a dev/ops utility (seed a
// fresh workspace, migrate a YAML change into an existing workspace),
// not a user-facing workflow action. Going DB-direct avoids adding a
// new HTTP endpoint (out of P1-8 scope) and matches the way the
// internal/workflow package tests already drive the service.
//
// DATABASE_URL selects the target database; MULTICA_WORKSPACE_ID /
// --workspace-id and --created-by fill the template's owning identity.
// The created template is a DRAFT — publish (and agent binding) is a
// separate step the operator runs next.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/workflow"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var workflowImportYAMLCmd = &cobra.Command{
	Use:   "import-yaml",
	Short: "Import a harness pipeline YAML as a draft workflow template",
	Long: `Import a harness pipeline YAML (e.g. harness/pipeline/standard.yaml)
into the workspace as a draft WorkflowTemplate.

The YAML schema maps onto the engine's node graph:
  role=planner/implementer → agent(executor)
  role=reviewer             → agent(evaluator)
  role=gate-runner          → gate(script)
  role=human                → acceptance
  human_gates.after_stage  → inserted acceptance
  + a trailing end node and linear edges.

This command writes a DRAFT template. Publish it separately (via the
template API or the workflow seed flow) once you have bound agents to
the agent/gate nodes — the YAML itself does not name agents.

Requires DATABASE_URL (defaults to the local dev postgres) and a
workspace + creator UUID. Does not call the HTTP API.`,
	RunE: runWorkflowImportYAML,
}

func init() {
	workflowCmd.AddCommand(workflowImportYAMLCmd)
	workflowImportYAMLCmd.Flags().String("file", "", "Path to the harness pipeline YAML (required)")
	workflowImportYAMLCmd.Flags().String("workspace", "", "Workspace UUID (env: MULTICA_WORKSPACE_ID)")
	workflowImportYAMLCmd.Flags().String("created-by", "", "Creator user UUID (env: MULTICA_USER_ID)")
	workflowImportYAMLCmd.Flags().String("key", "", "Template key override (defaults to pipeline.name)")
	workflowImportYAMLCmd.Flags().String("db-url", "", "PostgreSQL URL (env: DATABASE_URL; default "+defaultImportDBURL+")")
	workflowImportYAMLCmd.Flags().String("output", "text", "Output format: text or json")
	_ = workflowImportYAMLCmd.MarkFlagRequired("file")
}

// defaultImportDBURL mirrors the workflow package's test fixture default
// so an operator can run import-yaml against a fresh `make dev` setup
// without setting DATABASE_URL.
const defaultImportDBURL = "postgres://multica:multica@localhost:5432/multica?sslmode=disable"

// importYAMLCfg is the resolved flag set, separated from the cobra
// command for testability (tests construct one directly and call
// runImportYAMLWithCfg).
type importYAMLCfg struct {
	filePath   string
	workspace  string
	createdBy  string
	key        string
	dbURL      string
	output     string
}

// runWorkflowImportYAML is the cobra RunE adapter — resolves flags +
// env vars into an importYAMLCfg and delegates to runImportYAMLWithCfg.
func runWorkflowImportYAML(cmd *cobra.Command, _ []string) error {
	output, _ := cmd.Flags().GetString("output")
	if output != "text" && output != "json" {
		return fmt.Errorf("--output must be text or json, got %q", output)
	}
	filePath, _ := cmd.Flags().GetString("file")
	workspace, _ := cmd.Flags().GetString("workspace")
	if workspace == "" {
		workspace = os.Getenv("MULTICA_WORKSPACE_ID")
	}
	createdBy, _ := cmd.Flags().GetString("created-by")
	if createdBy == "" {
		createdBy = os.Getenv("MULTICA_USER_ID")
	}
	key, _ := cmd.Flags().GetString("key")
	dbURL, _ := cmd.Flags().GetString("db-url")
	if dbURL == "" {
		dbURL = os.Getenv("DATABASE_URL")
	}
	if dbURL == "" {
		dbURL = defaultImportDBURL
	}
	if workspace == "" {
		return fmt.Errorf("--workspace (or MULTICA_WORKSPACE_ID) is required")
	}
	if createdBy == "" {
		return fmt.Errorf("--created-by (or MULTICA_USER_ID) is required")
	}

	cfg := importYAMLCfg{
		filePath:  filePath,
		workspace: workspace,
		createdBy: createdBy,
		key:       key,
		dbURL:     dbURL,
		output:    output,
	}
	return runImportYAMLWithCfg(context.Background(), cfg, os.Stdout, os.Stderr)
}

// runImportYAMLWithCfg reads the YAML, opens the DB, runs the import,
// and prints the result. Split from runWorkflowImportYAML so tests can
// drive it with a temp YAML file and a test DB URL without invoking
// cobra. The Context lifetime covers the pool open + import; the pool
// is closed before return.
func runImportYAMLWithCfg(ctx context.Context, cfg importYAMLCfg, stdout, stderr io.Writer) error {
	raw, err := os.ReadFile(cfg.filePath)
	if err != nil {
		return fmt.Errorf("read --file: %w", err)
	}

	// Open pool with a bounded connect timeout — a misconfigured
	// DATABASE_URL should fail fast rather than hang the CLI.
	openCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(openCtx, cfg.dbURL)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer pool.Close()
	if err := pool.Ping(openCtx); err != nil {
		return fmt.Errorf("ping db: %w", err)
	}

	queries := db.New(pool)
	svc := workflow.NewTemplateService(queries, pool)

	importCtx, cancelImport := context.WithTimeout(ctx, 60*time.Second)
	defer cancelImport()
	detail, err := workflow.ImportYAMLFromBytes(importCtx, svc, raw, workflow.ImportYAMLParams{
		WorkspaceID: cfg.workspace,
		CreatedBy:   cfg.createdBy,
		Key:         cfg.key,
	})
	if err != nil {
		return err
	}

	switch cfg.output {
	case "json":
		return cli.PrintJSON(stdout, map[string]any{
			"template_id": util.UUIDToString(detail.Template.ID),
			"key":         detail.Template.Key,
			"name":        detail.Template.Name,
			"version":     detail.Template.Version,
			"node_count":  len(detail.Nodes),
			"edge_count":  len(detail.Edges),
			"status":      detail.Template.Status,
		})
	default:
		fmt.Fprintf(stdout, "imported  %s (template %s, v%d, %d nodes, %d edges, status=%s)\n",
			detail.Template.Key,
			util.UUIDToString(detail.Template.ID),
			detail.Template.Version,
			len(detail.Nodes),
			len(detail.Edges),
			detail.Template.Status,
		)
		// Surface node keys in text mode so the operator can verify the
		// shape without a separate call to the template API.
		for _, n := range detail.Nodes {
			fmt.Fprintf(stdout, "  - %-16s  %s\n", n.NodeKey, n.Type)
		}
	}
	_ = stderr // reserved for future --verbose diagnostics
	return nil
}
