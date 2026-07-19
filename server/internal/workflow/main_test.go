package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// main_test.go — shared fixtures for the workflow engine's DB-backed tests.
// Follows the service-package pattern (task_claim_race_test.go): every test
// gets its own workspace/user/agents and skips when Postgres is unreachable;
// `make test` provisions the DB via scripts/ensure-postgres.sh + migrate.

// testFixture bundles one isolated workspace with two runtime-backed agents
// (executor + evaluator — publish's produce/review separation requires they
// differ) plus the services the engine drives.
type testFixture struct {
	t           *testing.T
	pool        *pgxpool.Pool
	queries     *db.Queries
	bus         *events.Bus
	tasks       *service.TaskService
	issues      *service.IssueService
	engine      *Engine
	templates   *TemplateService
	workspaceID string
	userID      string
	memberID    string
	runtimeID   string
	executorID  string
	evaluatorID string
}

func newTestFixture(t *testing.T) *testFixture {
	t.Helper()

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://multica:multica@localhost:5432/multica?sslmode=disable"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Skipf("database unavailable: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("database unreachable: %v", err)
	}
	t.Cleanup(pool.Close)

	queries := db.New(pool)
	bus := events.New()
	tasks := service.NewTaskService(queries, pool, nil, bus)
	issues := service.NewIssueService(queries, pool, bus, nil, tasks)

	f := &testFixture{
		t:         t,
		pool:      pool,
		queries:   queries,
		bus:       bus,
		tasks:     tasks,
		issues:    issues,
		engine:    NewEngine(queries, pool, issues, tasks, bus),
		templates: NewTemplateService(queries, pool),
	}
	f.createIdentities()
	return f
}

func (f *testFixture) createIdentities() {
	f.t.Helper()
	ctx := context.Background()
	suffix := time.Now().UnixNano()

	err := f.pool.QueryRow(ctx, `
		INSERT INTO "user" (name, email) VALUES ($1, $2) RETURNING id
	`, "Workflow Test", fmt.Sprintf("workflow-test-%d@multica.ai", suffix)).Scan(&f.userID)
	if err != nil {
		f.t.Fatalf("create user: %v", err)
	}
	err = f.pool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ($1, $2, 'workflow engine test', 'WFT') RETURNING id
	`, "Workflow Test", fmt.Sprintf("workflow-test-%d", suffix)).Scan(&f.workspaceID)
	if err != nil {
		f.t.Fatalf("create workspace: %v", err)
	}
	err = f.pool.QueryRow(ctx, `
		INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'owner') RETURNING id
	`, f.workspaceID, f.userID).Scan(&f.memberID)
	if err != nil {
		f.t.Fatalf("create member: %v", err)
	}
	err = f.pool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status,
			device_info, metadata, last_seen_at, visibility, owner_id
		) VALUES ($1, NULL, $2, 'cloud', 'workflow_test', 'online', 'test runtime', '{}'::jsonb, now(), 'private', $3)
		RETURNING id
	`, f.workspaceID, "Workflow Test Runtime", f.userID).Scan(&f.runtimeID)
	if err != nil {
		f.t.Fatalf("create runtime: %v", err)
	}
	f.executorID = f.createAgent("Executor Agent")
	f.evaluatorID = f.createAgent("Evaluator Agent")

	f.t.Cleanup(func() {
		ctx := context.Background()
		f.pool.Exec(ctx, `DELETE FROM agent_task_queue WHERE agent_id IN ($1, $2)`, f.executorID, f.evaluatorID)
		f.pool.Exec(ctx, `DELETE FROM workflow_run WHERE workspace_id = $1`, f.workspaceID)
		f.pool.Exec(ctx, `DELETE FROM workflow_template WHERE workspace_id = $1`, f.workspaceID)
		f.pool.Exec(ctx, `DELETE FROM inbox_item WHERE workspace_id = $1`, f.workspaceID)
		f.pool.Exec(ctx, `DELETE FROM activity_log WHERE workspace_id = $1`, f.workspaceID)
		f.pool.Exec(ctx, `DELETE FROM issue WHERE workspace_id = $1`, f.workspaceID)
		f.pool.Exec(ctx, `DELETE FROM agent WHERE workspace_id = $1`, f.workspaceID)
		f.pool.Exec(ctx, `DELETE FROM agent_runtime WHERE workspace_id = $1`, f.workspaceID)
		f.pool.Exec(ctx, `DELETE FROM member WHERE workspace_id = $1`, f.workspaceID)
		f.pool.Exec(ctx, `DELETE FROM workspace WHERE id = $1`, f.workspaceID)
		f.pool.Exec(ctx, `DELETE FROM "user" WHERE id = $1`, f.userID)
	})
}

func (f *testFixture) createAgent(name string) string {
	f.t.Helper()
	var id string
	err := f.pool.QueryRow(context.Background(), `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, visibility, max_concurrent_tasks, owner_id
		) VALUES ($1, $2, '', 'cloud', '{}'::jsonb, $3, 'private', 1, $4)
		RETURNING id
	`, f.workspaceID, name, f.runtimeID, f.userID).Scan(&id)
	if err != nil {
		f.t.Fatalf("create agent %s: %v", name, err)
	}
	return id
}

// agentNode is a shorthand for an agent node input bound to a fixture agent
// by name (publish resolves the selector to a UUID).
func agentNode(key, role, agentName string, cfg NodeConfig) NodeInput {
	cfg.Role = role
	cfg.AgentSelector = agentName
	raw, err := json.Marshal(cfg)
	if err != nil {
		panic(err)
	}
	return NodeInput{NodeKey: key, Type: NodeTypeAgent, Name: key, Config: raw}
}

// typedNode builds an acceptance/end node input.
func typedNode(key, typ string, cfg NodeConfig) NodeInput {
	raw, err := json.Marshal(cfg)
	if err != nil {
		panic(err)
	}
	return NodeInput{NodeKey: key, Type: typ, Name: key, Config: raw}
}

// linearEdges chains node keys in order.
func linearEdges(keys ...string) []EdgeInput {
	var edges []EdgeInput
	for i := 0; i+1 < len(keys); i++ {
		edges = append(edges, EdgeInput{FromNodeKey: keys[i], ToNodeKey: keys[i+1]})
	}
	return edges
}

// createDraft builds an unpublished work→end draft template in the fixture
// workspace; fails the test on any error.
func (f *testFixture) createDraft(ctx context.Context, key string) *TemplateDetail {
	f.t.Helper()
	detail, err := f.templates.CreateTemplate(ctx, CreateTemplateParams{
		WorkspaceID: util.MustParseUUID(f.workspaceID),
		Key:         key,
		Name:        key,
		CreatedBy:   util.MustParseUUID(f.userID),
		Nodes: []NodeInput{
			agentNode("work", RoleExecutor, "Executor Agent", NodeConfig{}),
			typedNode("end", NodeTypeEnd, NodeConfig{}),
		},
		Edges: linearEdges("work", "end"),
	})
	if err != nil {
		f.t.Fatalf("create draft template: %v", err)
	}
	return detail
}

// createPublishedTemplate builds + publishes a template in the fixture
// workspace; fails the test on any error.
func (f *testFixture) createPublishedTemplate(key string, nodes []NodeInput, edges []EdgeInput) *TemplateDetail {
	f.t.Helper()
	detail, err := f.templates.CreateTemplate(context.Background(), CreateTemplateParams{
		WorkspaceID: util.MustParseUUID(f.workspaceID),
		Key:         key,
		Name:        key,
		CreatedBy:   util.MustParseUUID(f.userID),
		Nodes:       nodes,
		Edges:       edges,
	})
	if err != nil {
		f.t.Fatalf("create template: %v", err)
	}
	published, err := f.templates.PublishTemplate(context.Background(), util.MustParseUUID(f.workspaceID), detail.Template.ID)
	if err != nil {
		f.t.Fatalf("publish template: %v", err)
	}
	return published
}

// startRun starts a run for a template with the given external source id.
func (f *testFixture) startRun(tmpl *TemplateDetail, sourceID, title string) db.WorkflowRun {
	f.t.Helper()
	run, created, err := f.engine.StartRun(context.Background(), StartRunParams{
		WorkspaceID: util.MustParseUUID(f.workspaceID),
		TemplateID:  tmpl.Template.ID,
		SourceType:  "hook",
		SourceID:    sourceID,
		Title:       title,
		InitiatorID: util.MustParseUUID(f.userID),
	})
	if err != nil {
		f.t.Fatalf("start run: %v", err)
	}
	if !created {
		f.t.Fatalf("start run: expected created=true")
	}
	return run
}

// latestStep reads the newest step row for one node of a run.
func (f *testFixture) latestStep(runID pgtype.UUID, nodeKey string) db.StepInstance {
	f.t.Helper()
	step, err := f.queries.GetLatestStepInstanceForNode(context.Background(), db.GetLatestStepInstanceForNodeParams{
		RunID: runID, NodeKey: nodeKey,
	})
	if err != nil {
		f.t.Fatalf("latest step for %q: %v", nodeKey, err)
	}
	return step
}

// runStatus re-reads the run's status.
func (f *testFixture) runStatus(runID pgtype.UUID) string {
	f.t.Helper()
	run, err := f.queries.GetWorkflowRun(context.Background(), runID)
	if err != nil {
		f.t.Fatalf("get run: %v", err)
	}
	return run.Status
}

// passExecutorStep drives an executor step to a system-derived pass.
func (f *testFixture) passExecutorStep(runID pgtype.UUID, nodeKey string, exitFields map[string]any) {
	f.t.Helper()
	step := f.latestStep(runID, nodeKey)
	if !step.AgentTaskID.Valid {
		f.t.Fatalf("step %q has no task", nodeKey)
	}
	_, created, err := f.engine.RecordSubmission(context.Background(), step.AgentTaskID, SubmissionInput{
		Status:     SubmissionDone,
		ExitFields: exitFields,
	})
	if err != nil {
		f.t.Fatalf("record submission for %q: %v", nodeKey, err)
	}
	if !created {
		f.t.Fatalf("record submission for %q: expected created=true", nodeKey)
	}
}

// countTasksForIssue counts queued/dispatched/running tasks on an issue —
// the double-dispatch assertion (exactly one after activation).
func (f *testFixture) countTasksForIssue(issueID pgtype.UUID) int {
	f.t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM agent_task_queue WHERE issue_id = $1`, issueID).Scan(&n); err != nil {
		f.t.Fatalf("count tasks: %v", err)
	}
	return n
}

// inboxCount counts inbox rows of a type for the fixture workspace.
func (f *testFixture) inboxCount(typ string) int {
	f.t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM inbox_item WHERE workspace_id = $1 AND type = $2`, f.workspaceID, typ).Scan(&n); err != nil {
		f.t.Fatalf("count inbox: %v", err)
	}
	return n
}

// transitionsForStep lists a step's transition (from,to) pairs in order.
func (f *testFixture) transitionsForStep(stepID pgtype.UUID) [][2]string {
	f.t.Helper()
	rows, err := f.queries.ListStepTransitionsForStep(context.Background(), stepID)
	if err != nil {
		f.t.Fatalf("list transitions: %v", err)
	}
	out := make([][2]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, [2]string{r.FromStatus, r.ToStatus})
	}
	return out
}
