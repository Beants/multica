package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Pure graph-validation cases (no DB).

func TestValidateTemplateGraph(t *testing.T) {
	t.Parallel()
	node := func(key string) NodeInput {
		return agentNode(key, RoleExecutor, "Any Agent", NodeConfig{})
	}
	tests := []struct {
		name    string
		nodes   []NodeInput
		edges   []EdgeInput
		wantErr string
	}{
		{
			name:    "empty",
			nodes:   nil,
			wantErr: "at least one node",
		},
		{
			name:    "single node no edges",
			nodes:   []NodeInput{node("a")},
			edges:   nil,
			wantErr: "",
		},
		{
			name:    "linear three",
			nodes:   []NodeInput{node("a"), node("b"), node("c")},
			edges:   linearEdges("a", "b", "c"),
			wantErr: "",
		},
		{
			name:    "duplicate key",
			nodes:   []NodeInput{node("a"), node("a")},
			edges:   linearEdges("a", "a"),
			wantErr: "duplicate node_key",
		},
		{
			name:    "branch rejected (P0 linear)",
			nodes:   []NodeInput{node("a"), node("b"), node("c")},
			edges:   []EdgeInput{{FromNodeKey: "a", ToNodeKey: "b"}, {FromNodeKey: "a", ToNodeKey: "c"}},
			wantErr: "outgoing edges",
		},
		{
			name:    "cycle rejected",
			nodes:   []NodeInput{node("a"), node("b")},
			edges:   []EdgeInput{{FromNodeKey: "a", ToNodeKey: "b"}, {FromNodeKey: "b", ToNodeKey: "a"}},
			wantErr: "cycle detected",
		},
		{
			name:    "orphan node rejected",
			nodes:   []NodeInput{node("a"), node("b"), node("c")},
			edges:   linearEdges("a", "b"),
			wantErr: "exactly one start node",
		},
		{
			name:    "edge to unknown",
			nodes:   []NodeInput{node("a"), node("b")},
			edges:   []EdgeInput{{FromNodeKey: "a", ToNodeKey: "zzz"}},
			wantErr: "unknown node",
		},
		{
			name:    "gate type rejected",
			nodes:   []NodeInput{typedNode("g", "gate", NodeConfig{})},
			wantErr: "unsupported type",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTemplateGraph(tt.nodes, tt.edges)
			if tt.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if got := err.Error(); !strings.Contains(got, tt.wantErr) {
					t.Fatalf("error %q does not contain %q", got, tt.wantErr)
				}
			}
		})
	}
}

// Exit-fields validation semantics (D-9): missing required = structured
// error; unknown fields tolerated; type mismatch flagged; missing optional ok.

func TestValidateExitFields(t *testing.T) {
	t.Parallel()
	schema := &ExitFieldsSchema{Fields: []ExitFieldSpec{
		{Name: "pr_url", Type: "string", Required: true},
		{Name: "score", Type: "number"},
		{Name: "note", Type: "string"},
	}}

	t.Run("missing required is a structured error", func(t *testing.T) {
		errs := ValidateExitFields(schema, map[string]any{})
		if len(errs) != 1 || errs[0].Name != "pr_url" || errs[0].Code != "missing" {
			t.Fatalf("expected one missing pr_url error, got %+v", errs)
		}
	})

	t.Run("unknown fields tolerated", func(t *testing.T) {
		errs := ValidateExitFields(schema, map[string]any{
			"pr_url":      "https://example/pr/1",
			"extra_stuff": map[string]any{"nested": true},
		})
		if len(errs) != 0 {
			t.Fatalf("unknown fields must be tolerated, got %+v", errs)
		}
	})

	t.Run("type mismatch flagged", func(t *testing.T) {
		errs := ValidateExitFields(schema, map[string]any{
			"pr_url": "https://example/pr/1",
			"score":  "not-a-number",
		})
		if len(errs) != 1 || errs[0].Name != "score" || errs[0].Code != "type_mismatch" {
			t.Fatalf("expected score type_mismatch, got %+v", errs)
		}
	})

	t.Run("nil schema accepts anything", func(t *testing.T) {
		if errs := ValidateExitFields(nil, map[string]any{"x": 1}); len(errs) != 0 {
			t.Fatalf("nil schema must accept, got %+v", errs)
		}
	})
}

// Artifacts validation (D-11): durable references pass; local filesystem
// paths — the workdir GC would void them — are rejected with structured
// per-path errors.

func TestValidateArtifacts(t *testing.T) {
	t.Parallel()

	t.Run("durable references accepted", func(t *testing.T) {
		errs := ValidateArtifacts(json.RawMessage(`{
			"pr_url": "https://github.com/org/repo/pull/42",
			"branch": "feat/1234-workflow-engine",
			"attachment_id": "9b1deb4d-3b7d-4bad-9bdd-2b0d7b3dcb6d",
			"notes": ["review: https://ci.example/run/9", "concern-level"]
		}`))
		if len(errs) != 0 {
			t.Fatalf("durable references must pass, got %+v", errs)
		}
	})

	t.Run("nil and empty accepted", func(t *testing.T) {
		if errs := ValidateArtifacts(nil); len(errs) != 0 {
			t.Fatalf("nil artifacts must pass, got %+v", errs)
		}
		if errs := ValidateArtifacts(json.RawMessage(`{}`)); len(errs) != 0 {
			t.Fatalf("empty artifacts must pass, got %+v", errs)
		}
	})

	t.Run("local paths rejected", func(t *testing.T) {
		cases := map[string]string{
			"absolute posix":   "/Users/alice/multica_workspaces/ws/abc/output.md",
			"home relative":    "~/repo/output.md",
			"dot relative":     "./output.md",
			"dotdot relative":  "../shared/output.md",
			"windows drive":    `C:\Users\alice\output.md`,
			"windows slash":    "D:/build/output.md",
			"windows relative": `..\output.md`,
			"file url":         "file:///Users/alice/output.md",
			"workdir layout":   "multica_workspaces/ws-id/task-id/workdir/output.md",
			"workdir absolute": "/var/lib/daemon/workdir/output.md",
		}
		for name, path := range cases {
			raw, _ := json.Marshal(map[string]any{"file": path})
			errs := ValidateArtifacts(raw)
			if len(errs) != 1 || errs[0].Code != "local_path" || errs[0].Name != "artifacts.file" {
				t.Fatalf("%s: expected one local_path error at artifacts.file, got %+v", name, errs)
			}
		}
	})

	t.Run("nested offenders named by json path", func(t *testing.T) {
		errs := ValidateArtifacts(json.RawMessage(`{"files": ["https://ok.example/pr/1", {"diff": "/tmp/x.diff"}]}`))
		if len(errs) != 1 || errs[0].Name != "artifacts.files[1].diff" {
			t.Fatalf("expected nested path artifacts.files[1].diff, got %+v", errs)
		}
	})

	t.Run("branch literally named workdir stays legal", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]any{"branch": "workdir", "also": "fix/workdir-cleanup"})
		if errs := ValidateArtifacts(raw); len(errs) != 0 {
			t.Fatalf("branch names mentioning workdir must pass, got %+v", errs)
		}
	})

	t.Run("invalid json reported", func(t *testing.T) {
		errs := ValidateArtifacts(json.RawMessage(`{not json`))
		if len(errs) != 1 || errs[0].Code != "invalid_json" {
			t.Fatalf("expected invalid_json error, got %+v", errs)
		}
	})
}

// DB-backed template lifecycle tests.

func TestPublishFreezesAgentSelector(t *testing.T) {
	f := newTestFixture(t)
	ctx := context.Background()

	detail, err := f.templates.CreateTemplate(ctx, CreateTemplateParams{
		WorkspaceID: util.MustParseUUID(f.workspaceID),
		Key:         "freeze",
		Name:        "freeze",
		CreatedBy:   util.MustParseUUID(f.userID),
		Nodes: []NodeInput{
			agentNode("work", RoleExecutor, "Executor Agent", NodeConfig{Instructions: "do it"}),
			typedNode("end", NodeTypeEnd, NodeConfig{}),
		},
		Edges: linearEdges("work", "end"),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	published, err := f.templates.PublishTemplate(ctx, util.MustParseUUID(f.workspaceID), detail.Template.ID)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if published.Template.Status != "published" {
		t.Fatalf("status = %q, want published", published.Template.Status)
	}
	var cfg NodeConfig
	for _, n := range published.Nodes {
		if n.NodeKey == "work" {
			if err := json.Unmarshal(n.Config, &cfg); err != nil {
				t.Fatalf("unmarshal config: %v", err)
			}
		}
	}
	if cfg.AgentID != f.executorID {
		t.Fatalf("agent_id = %q, want frozen %q", cfg.AgentID, f.executorID)
	}
}

func TestPublishRejectsSharedEvaluatorAgent(t *testing.T) {
	f := newTestFixture(t)
	ctx := context.Background()

	// The evaluator node reuses the upstream executor's agent — pillar 5
	// produce/review separation must reject at publish.
	detail, err := f.templates.CreateTemplate(ctx, CreateTemplateParams{
		WorkspaceID: util.MustParseUUID(f.workspaceID),
		Key:         "separation",
		Name:        "separation",
		CreatedBy:   util.MustParseUUID(f.userID),
		Nodes: []NodeInput{
			agentNode("work", RoleExecutor, "Executor Agent", NodeConfig{}),
			agentNode("gate", RoleEvaluator, "Executor Agent", NodeConfig{}),
			typedNode("end", NodeTypeEnd, NodeConfig{}),
		},
		Edges: linearEdges("work", "gate", "end"),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_, err = f.templates.PublishTemplate(ctx, util.MustParseUUID(f.workspaceID), detail.Template.ID)
	var sepErr *EvaluatorSeparationError
	if !errors.As(err, &sepErr) {
		t.Fatalf("expected EvaluatorSeparationError, got %v", err)
	}
	if sepErr.NodeKey != "gate" || sepErr.UpstreamKey != "work" {
		t.Fatalf("separation error names %q/%q, want gate/work", sepErr.NodeKey, sepErr.UpstreamKey)
	}
}

func TestPublishArchivesPreviousVersion(t *testing.T) {
	f := newTestFixture(t)
	ctx := context.Background()
	wsID := util.MustParseUUID(f.workspaceID)

	v1, err := f.templates.PublishTemplate(ctx, wsID, f.createDraft(ctx, "versioned").Template.ID)
	if err != nil {
		t.Fatalf("publish v1: %v", err)
	}

	// Publishing a key with an existing published version goes through a new
	// version row: fork the next draft (version = MAX+1) and publish it.
	fork, err := f.templates.CreateTemplateVersion(ctx, wsID, v1.Template.ID, "", util.MustParseUUID(f.userID))
	if err != nil {
		t.Fatalf("create v2 draft: %v", err)
	}
	v2, err := f.templates.PublishTemplate(ctx, wsID, fork.Template.ID)
	if err != nil {
		t.Fatalf("publish v2: %v", err)
	}
	if v2.Template.Version != v1.Template.Version+1 {
		t.Fatalf("v2 version = %d, want %d", v2.Template.Version, v1.Template.Version+1)
	}

	// v1 must be archived now (one published per key).
	old, err := f.queries.GetWorkflowTemplate(ctx, v1.Template.ID)
	if err != nil {
		t.Fatalf("get v1: %v", err)
	}
	if old.Status != "archived" {
		t.Fatalf("v1 status = %q, want archived", old.Status)
	}
	// Resolving the published template by key returns v2.
	got, err := f.queries.GetPublishedWorkflowTemplateByKey(ctx, db.GetPublishedWorkflowTemplateByKeyParams{
		WorkspaceID: wsID, Key: "versioned",
	})
	if err != nil {
		t.Fatalf("get published by key: %v", err)
	}
	if got.ID != v2.Template.ID {
		t.Fatalf("published by key = %v, want v2 %v", got.ID, v2.Template.ID)
	}
}

func TestCreateTemplateVersionCopiesGraph(t *testing.T) {
	f := newTestFixture(t)
	ctx := context.Background()
	wsID := util.MustParseUUID(f.workspaceID)

	src := f.createPublishedTemplate("forked", []NodeInput{
		agentNode("work", RoleExecutor, "Executor Agent", NodeConfig{Instructions: "build"}),
		agentNode("gate", RoleEvaluator, "Evaluator Agent", NodeConfig{}),
		typedNode("end", NodeTypeEnd, NodeConfig{}),
	}, linearEdges("work", "gate", "end"))

	fork, err := f.templates.CreateTemplateVersion(ctx, wsID, src.Template.ID, "", util.MustParseUUID(f.userID))
	if err != nil {
		t.Fatalf("create version: %v", err)
	}
	if fork.Template.Version != src.Template.Version+1 {
		t.Fatalf("version = %d, want %d", fork.Template.Version, src.Template.Version+1)
	}
	if fork.Template.Status != "draft" {
		t.Fatalf("status = %q, want draft", fork.Template.Status)
	}
	if fork.Template.Key != "forked" || fork.Template.Name != src.Template.Name {
		t.Fatalf("key/name = %q/%q, want forked/%q", fork.Template.Key, fork.Template.Name, src.Template.Name)
	}
	if len(fork.Nodes) != len(src.Nodes) || len(fork.Edges) != len(src.Edges) {
		t.Fatalf("graph sizes = %d nodes/%d edges, want %d/%d",
			len(fork.Nodes), len(fork.Edges), len(src.Nodes), len(src.Edges))
	}
	// Node keys and configs carry over verbatim (frozen agent_id included).
	byKey := map[string]db.WorkflowNode{}
	for _, n := range fork.Nodes {
		byKey[n.NodeKey] = n
	}
	work, ok := byKey["work"]
	if !ok {
		t.Fatalf("fork missing node work")
	}
	cfg, err := ParseNodeConfig(work.Config)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	if cfg.AgentID != f.executorID {
		t.Fatalf("forked agent_id = %q, want %q", cfg.AgentID, f.executorID)
	}
	// The fork is publishable and archives the source version.
	if _, err := f.templates.PublishTemplate(ctx, wsID, fork.Template.ID); err != nil {
		t.Fatalf("publish fork: %v", err)
	}
	old, err := f.queries.GetWorkflowTemplate(ctx, src.Template.ID)
	if err != nil {
		t.Fatalf("get src: %v", err)
	}
	if old.Status != "archived" {
		t.Fatalf("src status = %q, want archived after fork publish", old.Status)
	}
}

func TestUpdatePublishedTemplateRejected(t *testing.T) {
	f := newTestFixture(t)
	ctx := context.Background()
	tmpl := f.createPublishedTemplate("immutable", []NodeInput{
		agentNode("work", RoleExecutor, "Executor Agent", NodeConfig{}),
		typedNode("end", NodeTypeEnd, NodeConfig{}),
	}, linearEdges("work", "end"))

	name := "new name"
	_, err := f.templates.UpdateTemplate(ctx, UpdateTemplateParams{
		WorkspaceID: util.MustParseUUID(f.workspaceID),
		TemplateID:  tmpl.Template.ID,
		Name:        &name,
	})
	if !errors.Is(err, ErrTemplateNotDraft) {
		t.Fatalf("expected ErrTemplateNotDraft, got %v", err)
	}
}
