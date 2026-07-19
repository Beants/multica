// Package workflow implements the P0 linear workflow engine (in-repo fork,
// blueprint decision D3): template CRUD + publish, run state machine,
// activation/dispatch, and targeted rework. Concurrency rules follow
// blueprint §8 (guarded status updates + hard UNIQUE constraints); the
// engine never evaluates edge conditions in P0 — edges carry condition=NULL
// and progression takes the single default edge.
package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Node types (workflow_node.type CHECK). fan_out/converge are accepted by
// the DAG validator (P1-1 Wave 0) but not yet dispatched by activateNode
// (Wave 2 adds the case branches). gate remains P1+ forward-compat only.
const (
	NodeTypeAgent      = "agent"
	NodeTypeAcceptance = "acceptance"
	NodeTypeEnd        = "end"
	NodeTypeFanOut     = "fan_out"
	NodeTypeConverge   = "converge"
)

// Node roles (node config.role). executor is the default when unset.
const (
	RoleExecutor  = "executor"
	RoleEvaluator = "evaluator"
	RoleReviewer  = "reviewer"
)

// Fail policies for fan_out node config (NodeConfig.FailPolicy). Empty /
// unset is treated as FailPolicyRework by EffectiveFailPolicy.
const (
	FailPolicyFail    = "fail"    // any child fails → all siblings skipped, run failed
	FailPolicyBlocked = "blocked" // any child fails/blocked → run blocked, inbox reviewer
	FailPolicyRework  = "rework"  // failed child attempt++ (siblings unaffected) — default
)

const defaultMaxAttempts = 3

// ExitFieldSpec declares one expected exit field on a node. Type is a JSON
// type name: string | number | boolean | object | array | any (empty = any).
type ExitFieldSpec struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Required    bool   `json:"required,omitempty"`
	Description string `json:"description,omitempty"`
}

// ExitFieldsSchema is the per-node 准出 schema frozen into the run snapshot.
type ExitFieldsSchema struct {
	Fields []ExitFieldSpec `json:"fields"`
}

// NodeConfig is the per-node JSONB config shape. AgentSelector is the
// human-authored reference (UUID or agent name); publish resolves it once
// into AgentID (D-7: no fuzzy resolution at runtime).
type NodeConfig struct {
	Role          string            `json:"role,omitempty"`
	AgentSelector string            `json:"agent_selector,omitempty"`
	AgentID       string            `json:"agent_id,omitempty"`
	Instructions  string            `json:"instructions,omitempty"`
	ExitFields    *ExitFieldsSchema `json:"exit_fields,omitempty"`
	MaxAttempts   int32             `json:"max_attempts,omitempty"`
	AutoPass      bool              `json:"auto_pass,omitempty"`
	// ReviewerID optionally names the member (member.id) an acceptance node
	// notifies at activation. The hook payload's reviewer (run context) takes
	// precedence; this is the template-level default.
	ReviewerID string `json:"reviewer_id,omitempty"`

	// ItemsField names the array exit_field on the upstream node that fan_out
	// consumes to expand N child steps. Required for fan_out nodes; ignored
	// elsewhere. The referenced field must exist on the upstream node's
	// ExitFieldsSchema with type=array (validated at publish).
	ItemsField string `json:"items_field,omitempty"`

	// FailPolicy governs how fan_out handles a failed child step. Default
	// (empty / unset, see EffectiveFailPolicy) is FailPolicyRework. Only
	// meaningful on fan_out nodes.
	FailPolicy string `json:"fail_policy,omitempty"`
}

// EffectiveRole defaults an unset role to executor (design.md §4.3).
func (c NodeConfig) EffectiveRole() string {
	if c.Role == "" {
		return RoleExecutor
	}
	return c.Role
}

// EffectiveMaxAttempts applies the P0 default retry budget.
func (c NodeConfig) EffectiveMaxAttempts() int32 {
	if c.MaxAttempts <= 0 {
		return defaultMaxAttempts
	}
	return c.MaxAttempts
}

// EffectiveFailPolicy applies the fan_out default fail policy. Empty /
// unset → FailPolicyRework; any explicit value is returned as-is (the
// enum was validated at ParseNodeConfig time).
func (c NodeConfig) EffectiveFailPolicy() string {
	if c.FailPolicy == "" {
		return FailPolicyRework
	}
	return c.FailPolicy
}

// ParseNodeConfig decodes a node config blob, tolerating unknown fields
// (forward-compat D-9). Nil/empty input yields the zero config.
func ParseNodeConfig(raw []byte) (NodeConfig, error) {
	var cfg NodeConfig
	if len(raw) == 0 {
		return cfg, nil
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return NodeConfig{}, fmt.Errorf("node config: %w", err)
	}
	switch cfg.EffectiveRole() {
	case RoleExecutor, RoleEvaluator, RoleReviewer:
	default:
		return NodeConfig{}, fmt.Errorf("node config: unknown role %q", cfg.Role)
	}
	if cfg.ExitFields != nil {
		for _, f := range cfg.ExitFields.Fields {
			if f.Name == "" {
				return NodeConfig{}, errors.New("node config: exit_fields entry with empty name")
			}
			switch f.Type {
			case "", "any", "string", "number", "boolean", "object", "array":
			default:
				return NodeConfig{}, fmt.Errorf("node config: exit field %q has unknown type %q", f.Name, f.Type)
			}
		}
	}
	switch cfg.FailPolicy {
	case "", FailPolicyFail, FailPolicyBlocked, FailPolicyRework:
	default:
		return NodeConfig{}, fmt.Errorf("node config: unknown fail_policy %q", cfg.FailPolicy)
	}
	return cfg, nil
}

// ---------------------------------------------------------------------------
// Template snapshot — frozen into workflow_run.template_snapshot at StartRun.
// The published template's rows are immutable (draft-only UPDATE guards), so
// a snapshot built at run start never diverges from what publish validated.
// ---------------------------------------------------------------------------

// SnapshotNode is one frozen node. NodeKey replaces the row UUID: step rows
// reference node_key because the snapshot is JSONB, not live rows.
type SnapshotNode struct {
	NodeKey string     `json:"node_key"`
	Type    string     `json:"type"`
	Name    string     `json:"name"`
	Config  NodeConfig `json:"config"`
}

// SnapshotEdge is one frozen edge. Condition is omitted: P0 edges are always
// condition=NULL and the engine takes the default edge by priority.
type SnapshotEdge struct {
	FromNodeKey string `json:"from_node_key"`
	ToNodeKey   string `json:"to_node_key"`
	Priority    int32  `json:"priority"`
}

// Snapshot is the frozen template graph carried by every run.
type Snapshot struct {
	TemplateID string         `json:"template_id"`
	Key        string         `json:"key"`
	Version    int32          `json:"version"`
	Nodes      []SnapshotNode `json:"nodes"`
	Edges      []SnapshotEdge `json:"edges"`
}

// ParseSnapshot decodes a run's template_snapshot blob.
func ParseSnapshot(raw []byte) (*Snapshot, error) {
	var snap Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, fmt.Errorf("template snapshot: %w", err)
	}
	return &snap, nil
}

// NodeByKey returns the snapshot node with the given key, or nil.
func (s *Snapshot) NodeByKey(key string) *SnapshotNode {
	for i := range s.Nodes {
		if s.Nodes[i].NodeKey == key {
			return &s.Nodes[i]
		}
	}
	return nil
}

// StartNode returns the single in-degree-0 node, or nil when the graph is
// malformed (publish validation guarantees exactly one).
func (s *Snapshot) StartNode() *SnapshotNode {
	inDegree := map[string]int{}
	for _, e := range s.Edges {
		inDegree[e.ToNodeKey]++
	}
	for i := range s.Nodes {
		if inDegree[s.Nodes[i].NodeKey] == 0 {
			return &s.Nodes[i]
		}
	}
	return nil
}

// UpstreamNodeKeys returns all transitive ancestors of nodeKey (used by
// publish's evaluator-separation check and step-context assembly).
func (s *Snapshot) UpstreamNodeKeys(nodeKey string) []string {
	predecessors := map[string][]string{}
	for _, e := range s.Edges {
		predecessors[e.ToNodeKey] = append(predecessors[e.ToNodeKey], e.FromNodeKey)
	}
	seen := map[string]bool{}
	queue := append([]string{}, predecessors[nodeKey]...)
	var out []string
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if seen[cur] {
			continue
		}
		seen[cur] = true
		out = append(out, cur)
		queue = append(queue, predecessors[cur]...)
	}
	return out
}

// NextAfterAll returns every node reached from nodeKey by an outgoing edge,
// sorted by priority ascending (ties broken by ToNodeKey for stability). P0
// linear templates have out-degree 1, so the slice length is exactly 1 for
// mid-chain nodes and 0 at the chain tail — the P0 single-next semantics
// are recovered as `NextAfterAll(x)[0]` (callers must nil-check the slice).
//
// P0 edges carry condition=NULL; the DAG validator (Wave 0) admits fan_out
// branching, so a fan_out node's NextAfterAll may return N > 1 elements.
func (s *Snapshot) NextAfterAll(nodeKey string) []SnapshotNode {
	type cand struct {
		idx      int
		priority int32
		toKey    string
	}
	var cands []cand
	for i, e := range s.Edges {
		if e.FromNodeKey != nodeKey {
			continue
		}
		cands = append(cands, cand{idx: i, priority: e.Priority, toKey: e.ToNodeKey})
	}
	// Stable order: priority asc, then ToNodeKey asc — deterministic across
	// equal-priority edges so test assertions and downstream activation are
	// reproducible.
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].priority != cands[j].priority {
			return cands[i].priority < cands[j].priority
		}
		return cands[i].toKey < cands[j].toKey
	})
	out := make([]SnapshotNode, 0, len(cands))
	for _, c := range cands {
		if n := s.NodeByKey(s.Edges[c.idx].ToNodeKey); n != nil {
			out = append(out, *n)
		}
	}
	return out
}

// DownstreamNodeKeys returns every transitive descendant of nodeKey in the
// DAG (exclusive of nodeKey itself), discovered by BFS over NextAfterAll.
// The visited map makes the walk cycle-safe even when the graph is
// malformed at runtime (publish validation rejects cycles, but a hand-edited
// snapshot could still loop).
//
// Rework invalidates exactly this set (design.md §4.4). For P0 linear
// templates the BFS degenerates to the original chain walk and returns the
// same node list, in chain order.
func (s *Snapshot) DownstreamNodeKeys(nodeKey string) []string {
	visited := map[string]bool{nodeKey: true}
	var out []string
	queue := []string{nodeKey}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, next := range s.NextAfterAll(cur) {
			if visited[next.NodeKey] {
				continue
			}
			visited[next.NodeKey] = true
			out = append(out, next.NodeKey)
			queue = append(queue, next.NodeKey)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Template service
// ---------------------------------------------------------------------------

// TxStarter abstracts transaction creation (satisfied by pgxpool.Pool).
// Mirrors service.TxStarter; redeclared so this package does not depend on
// the service package for template-only callers.
type TxStarter interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Sentinel errors mapped to HTTP codes by handlers.
var (
	ErrTemplateNotFound     = errors.New("workflow: template not found")
	ErrTemplateNotDraft     = errors.New("workflow: template is not a draft")
	ErrTemplateNotPublished = errors.New("workflow: template is not published")
	ErrTemplateConflict     = errors.New("workflow: template changed concurrently")
	ErrAgentNotFound        = errors.New("workflow: agent selector does not resolve to a workspace agent")
	ErrAgentAmbiguous       = errors.New("workflow: agent selector matches multiple agents")
)

// EvaluatorSeparationError reports a publish-time violation of the
// produce/review separation rule (blueprint pillar 5): an evaluator-role
// node resolved to the same agent as an upstream executor node.
type EvaluatorSeparationError struct {
	NodeKey     string
	UpstreamKey string
	AgentID     string
}

func (e *EvaluatorSeparationError) Error() string {
	return fmt.Sprintf("workflow: evaluator node %q must not use the same agent as upstream executor node %q (%s)",
		e.NodeKey, e.UpstreamKey, e.AgentID)
}

// NodeInput is the service-layer shape for one node in a create/update call.
type NodeInput struct {
	NodeKey  string
	Type     string
	Name     string
	Config   json.RawMessage
	Position json.RawMessage
}

// EdgeInput is one edge in a create/update call, keyed by node_key (row IDs
// are assigned at insert time).
type EdgeInput struct {
	FromNodeKey string
	ToNodeKey   string
	Priority    int32
}

// TemplateDetail is the full template read model (template + graph).
type TemplateDetail struct {
	Template db.WorkflowTemplate
	Nodes    []db.WorkflowNode
	Edges    []db.WorkflowEdge
}

// TemplateService owns template CRUD + publish. Publish freezes the graph:
// it resolves every agent_selector to a concrete agent UUID written back
// into the node config (D-7), validates evaluator/upstream-executor
// separation, and flips status draft → published under a guarded UPDATE.
type TemplateService struct {
	Queries   *db.Queries
	TxStarter TxStarter
}

func NewTemplateService(q *db.Queries, tx TxStarter) *TemplateService {
	return &TemplateService{Queries: q, TxStarter: tx}
}

// validateTemplateGraph enforces the P1-1 DAG shape: P0 type set
// (agent/acceptance/end) plus fan_out/converge, exactly one start node
// (in-degree 0), no cycles, no orphans, and per-type out-degree rules.
//
// Out-degree rules:
//   - fan_out: ≥ 1 (the branching node — may have many downstreams)
//   - converge: == 1 (single downstream; multi-inbound is the converge
//     semantics, but the post-converge chain is linear)
//   - agent / acceptance: == 1 (linear pass-through)
//   - end: == 0 (terminal)
//
// In-degree rules: converge allows ≥ 1 inbound (the multi-inbound case is
// the AND-join; Wave 1's ValidateConvergePairing enforces the
// fan_out↔converge pairing on top of this shape check). All other types
// expect exactly 1 inbound except the single start node (in-degree 0).
//
// Wave 1 also enforces fan_out config semantics via ValidateFanOutConfig
// (items_field must point at an array field declared on an upstream node).
//
// P0 linear templates satisfy this unchanged (out-degree 1 everywhere
// except the end node), so the upgrade is backward-compatible.
func validateTemplateGraph(nodes []NodeInput, edges []EdgeInput) error {
	if len(nodes) == 0 {
		return errors.New("workflow: at least one node is required")
	}
	keys := make(map[string]bool, len(nodes))
	cfgByKey := make(map[string]NodeConfig, len(nodes))
	for _, n := range nodes {
		if n.NodeKey == "" {
			return errors.New("workflow: node_key is required")
		}
		if keys[n.NodeKey] {
			return fmt.Errorf("workflow: duplicate node_key %q", n.NodeKey)
		}
		keys[n.NodeKey] = true
		switch n.Type {
		case NodeTypeAgent, NodeTypeAcceptance, NodeTypeEnd, NodeTypeFanOut, NodeTypeConverge:
		default:
			return fmt.Errorf("workflow: node %q has unsupported type %q", n.NodeKey, n.Type)
		}
		if n.Name == "" {
			return fmt.Errorf("workflow: node %q requires a name", n.NodeKey)
		}
		cfg, err := ParseNodeConfig(n.Config)
		if err != nil {
			return fmt.Errorf("workflow: node %q: %w", n.NodeKey, err)
		}
		cfgByKey[n.NodeKey] = cfg
		if n.Type == NodeTypeAgent && cfg.AgentSelector == "" && cfg.AgentID == "" {
			return fmt.Errorf("workflow: agent node %q requires agent_selector", n.NodeKey)
		}
	}
	inDegree := map[string]int{}
	outDegree := map[string]int{}
	for _, e := range edges {
		if !keys[e.FromNodeKey] {
			return fmt.Errorf("workflow: edge from unknown node %q", e.FromNodeKey)
		}
		if !keys[e.ToNodeKey] {
			return fmt.Errorf("workflow: edge to unknown node %q", e.ToNodeKey)
		}
		if e.FromNodeKey == e.ToNodeKey {
			return fmt.Errorf("workflow: node %q cannot edge to itself", e.FromNodeKey)
		}
		inDegree[e.ToNodeKey]++
		outDegree[e.FromNodeKey]++
	}
	// Per-type out-degree check. fan_out is the only branching type; every
	// other type keeps P0's out-degree ≤ 1 invariant (0 = chain tail).
	for _, n := range nodes {
		got := outDegree[n.NodeKey]
		switch n.Type {
		case NodeTypeFanOut:
			if got < 1 {
				return fmt.Errorf("workflow: fan_out node %q requires at least 1 outgoing edge", n.NodeKey)
			}
		default:
			if got > 1 {
				return fmt.Errorf("workflow: node %q has %d outgoing edges, want at most 1 (only fan_out may branch)", n.NodeKey, got)
			}
		}
	}
	// Wave 1: fan_out config (items_field + upstream array declaration).
	// Runs AFTER the out-degree check so a branchless fan_out reports its
	// structural problem before its config problem (tighter error message).
	for _, n := range nodes {
		if n.Type != NodeTypeFanOut {
			continue
		}
		if err := ValidateFanOutConfig(n, cfgByKey[n.NodeKey], nodes, edges); err != nil {
			return err
		}
	}
	// Exactly one start node (in-degree 0). Zero starts means the graph is
	// one big cycle (every node has at least one inbound edge).
	var starts []string
	for _, n := range nodes {
		if inDegree[n.NodeKey] == 0 {
			starts = append(starts, n.NodeKey)
		}
	}
	switch len(starts) {
	case 1:
		// ok
	case 0:
		return errors.New("workflow: template has no start node (every node has an inbound edge — cycle detected)")
	default:
		return fmt.Errorf("workflow: template must have exactly one start node, found %d", len(starts))
	}
	// BFS from the start: every node must be reachable (no orphans), and
	// any back-edge (a node already fully processed in DFS terms) means a
	// cycle. Use color marking: white=unseen, grey=on-stack, black=done.
	adj := map[string][]string{}
	for _, e := range edges {
		adj[e.FromNodeKey] = append(adj[e.FromNodeKey], e.ToNodeKey)
	}
	const (
		white = 0
		grey  = 1
		black = 2
	)
	color := map[string]int{}
	var dfs func(key string) error
	dfs = func(key string) error {
		color[key] = grey
		for _, next := range adj[key] {
			switch color[next] {
			case white:
				if err := dfs(next); err != nil {
					return err
				}
			case grey:
				return fmt.Errorf("workflow: cycle detected at node %q (edge %q → %q)", next, key, next)
			}
		}
		color[key] = black
		return nil
	}
	if err := dfs(starts[0]); err != nil {
		return err
	}
	for _, n := range nodes {
		if color[n.NodeKey] == white {
			return fmt.Errorf("workflow: node %q is unreachable from the start node", n.NodeKey)
		}
	}
	// Wave 1: fan_out↔converge pairing. Runs after the structural graph
	// check so cycle/orphan errors surface first.
	if err := ValidateConvergePairing(nodes, edges); err != nil {
		return err
	}
	return nil
}

// CreateTemplateParams carries one template create. Key is the immutable
// template key; new versions of the same key arrive as separate creates.
type CreateTemplateParams struct {
	WorkspaceID pgtype.UUID
	Key         string
	Name        string
	Description string
	CreatedBy   pgtype.UUID
	Nodes       []NodeInput
	Edges       []EdgeInput
}

// CreateTemplate writes a draft template with its full graph in one tx.
func (s *TemplateService) CreateTemplate(ctx context.Context, p CreateTemplateParams) (*TemplateDetail, error) {
	if p.Key == "" {
		return nil, errors.New("workflow: template key is required")
	}
	if p.Name == "" {
		return nil, errors.New("workflow: template name is required")
	}
	if err := validateTemplateGraph(p.Nodes, p.Edges); err != nil {
		return nil, err
	}

	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := s.Queries.WithTx(tx)

	tmpl, err := qtx.CreateWorkflowTemplate(ctx, db.CreateWorkflowTemplateParams{
		WorkspaceID: p.WorkspaceID,
		Key:         p.Key,
		Name:        p.Name,
		Description: p.Description,
		CreatedBy:   p.CreatedBy,
	})
	if err != nil {
		return nil, fmt.Errorf("create template: %w", err)
	}
	nodes, edges, err := insertGraph(ctx, qtx, tmpl.ID, p.Nodes, p.Edges)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return &TemplateDetail{Template: tmpl, Nodes: nodes, Edges: edges}, nil
}

// insertGraph inserts nodes (mapping node_key → row UUID) then edges.
func insertGraph(ctx context.Context, qtx *db.Queries, templateID pgtype.UUID, in []NodeInput, edgesIn []EdgeInput) ([]db.WorkflowNode, []db.WorkflowEdge, error) {
	idByKey := make(map[string]pgtype.UUID, len(in))
	nodes := make([]db.WorkflowNode, 0, len(in))
	for _, n := range in {
		config := n.Config
		if len(config) == 0 {
			config = []byte("{}")
		}
		row, err := qtx.CreateWorkflowNode(ctx, db.CreateWorkflowNodeParams{
			TemplateID: templateID,
			NodeKey:    n.NodeKey,
			Type:       n.Type,
			Name:       n.Name,
			Config:     config,
			Position:   n.Position,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("create node %q: %w", n.NodeKey, err)
		}
		idByKey[n.NodeKey] = row.ID
		nodes = append(nodes, row)
	}
	edges := make([]db.WorkflowEdge, 0, len(edgesIn))
	for _, e := range edgesIn {
		row, err := qtx.CreateWorkflowEdge(ctx, db.CreateWorkflowEdgeParams{
			TemplateID: templateID,
			FromNodeID: idByKey[e.FromNodeKey],
			ToNodeID:   idByKey[e.ToNodeKey],
			Priority:   e.Priority,
			Condition:  nil, // P0: always NULL (design.md §1)
		})
		if err != nil {
			return nil, nil, fmt.Errorf("create edge %q → %q: %w", e.FromNodeKey, e.ToNodeKey, err)
		}
		edges = append(edges, row)
	}
	return nodes, edges, nil
}

// GetTemplate reads one template with its graph, scoped to the workspace.
func (s *TemplateService) GetTemplate(ctx context.Context, workspaceID, templateID pgtype.UUID) (*TemplateDetail, error) {
	tmpl, err := s.Queries.GetWorkflowTemplateInWorkspace(ctx, db.GetWorkflowTemplateInWorkspaceParams{
		ID: templateID, WorkspaceID: workspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrTemplateNotFound
		}
		return nil, fmt.Errorf("get template: %w", err)
	}
	nodes, err := s.Queries.ListWorkflowNodes(ctx, tmpl.ID)
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	edges, err := s.Queries.ListWorkflowEdgesForTemplate(ctx, tmpl.ID)
	if err != nil {
		return nil, fmt.Errorf("list edges: %w", err)
	}
	return &TemplateDetail{Template: tmpl, Nodes: nodes, Edges: edges}, nil
}

// ListTemplates lists all templates in a workspace (any status).
func (s *TemplateService) ListTemplates(ctx context.Context, workspaceID pgtype.UUID) ([]db.WorkflowTemplate, error) {
	return s.Queries.ListWorkflowTemplates(ctx, workspaceID)
}

// UpdateTemplateParams is a draft-only edit. Nodes/Edges non-nil rewrites
// the graph wholesale (edges cascade via FK on node delete).
type UpdateTemplateParams struct {
	WorkspaceID pgtype.UUID
	TemplateID  pgtype.UUID
	Name        *string
	Description *string
	Nodes       []NodeInput
	Edges       []EdgeInput
	// ReplaceGraph, when true, rewrites nodes+edges from Nodes/Edges.
	ReplaceGraph bool
}

// UpdateTemplate edits a draft. The draft-only guard lives in the UPDATE
// query itself; a concurrent publish turns the edit into ErrTemplateNotDraft.
func (s *TemplateService) UpdateTemplate(ctx context.Context, p UpdateTemplateParams) (*TemplateDetail, error) {
	if p.ReplaceGraph {
		if err := validateTemplateGraph(p.Nodes, p.Edges); err != nil {
			return nil, err
		}
	}
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := s.Queries.WithTx(tx)

	tmpl, err := qtx.UpdateWorkflowTemplate(ctx, db.UpdateWorkflowTemplateParams{
		ID:          p.TemplateID,
		Name:        util.PtrToText(p.Name),
		Description: util.PtrToText(p.Description),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrTemplateNotDraft
		}
		return nil, fmt.Errorf("update template: %w", err)
	}
	if tmpl.WorkspaceID != p.WorkspaceID {
		return nil, ErrTemplateNotFound
	}
	if p.ReplaceGraph {
		// Nodes cascade their edges via FK; delete edges first anyway so the
		// rewrite does not depend on cascade timing.
		if err := qtx.DeleteWorkflowEdgesForTemplate(ctx, tmpl.ID); err != nil {
			return nil, fmt.Errorf("clear edges: %w", err)
		}
		if err := qtx.DeleteWorkflowNodesForTemplate(ctx, tmpl.ID); err != nil {
			return nil, fmt.Errorf("clear nodes: %w", err)
		}
		if _, _, err := insertGraph(ctx, qtx, tmpl.ID, p.Nodes, p.Edges); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return s.GetTemplate(ctx, p.WorkspaceID, tmpl.ID)
}

// ArchiveTemplate retires a template (draft or published). Archived
// templates keep their runs auditable (workflow_run.template_id is
// RESTRICT); the guarded UPDATE makes a double-archive a conflict.
func (s *TemplateService) ArchiveTemplate(ctx context.Context, workspaceID, templateID pgtype.UUID) (db.WorkflowTemplate, error) {
	if _, err := s.GetTemplate(ctx, workspaceID, templateID); err != nil {
		return db.WorkflowTemplate{}, err
	}
	for _, expected := range []string{"published", "draft"} {
		tmpl, err := s.Queries.UpdateWorkflowTemplateStatus(ctx, db.UpdateWorkflowTemplateStatusParams{
			NewStatus: "archived", ID: templateID, ExpectedStatus: expected,
		})
		if err == nil {
			return tmpl, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return db.WorkflowTemplate{}, fmt.Errorf("archive template: %w", err)
		}
	}
	return db.WorkflowTemplate{}, ErrTemplateConflict
}

// CreateTemplateVersion forks a new draft at version+1 from an existing
// template (draft or published), copying the full node/edge graph. This is
// the only way to "edit" a published template — published rows stay
// immutable so their run snapshots never drift (design.md §1). The copy
// carries configs verbatim: a forked published template already has frozen
// agent UUIDs, which the next publish re-validates anyway.
func (s *TemplateService) CreateTemplateVersion(ctx context.Context, workspaceID, templateID pgtype.UUID, name string, createdBy pgtype.UUID) (*TemplateDetail, error) {
	src, err := s.GetTemplate(ctx, workspaceID, templateID)
	if err != nil {
		return nil, err
	}
	if src.Template.Status == "archived" {
		return nil, errors.New("workflow: archived templates cannot be versioned")
	}
	if name == "" {
		name = src.Template.Name
	}

	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := s.Queries.WithTx(tx)

	fresh, err := qtx.CreateTemplateVersion(ctx, db.CreateTemplateVersionParams{
		WorkspaceID: workspaceID,
		Key:         src.Template.Key,
		Name:        name,
		Description: src.Template.Description,
		CreatedBy:   createdBy,
	})
	if err != nil {
		return nil, fmt.Errorf("create template version: %w", err)
	}
	idByKey := make(map[string]pgtype.UUID, len(src.Nodes))
	for _, n := range src.Nodes {
		row, err := qtx.CreateWorkflowNode(ctx, db.CreateWorkflowNodeParams{
			TemplateID: fresh.ID,
			NodeKey:    n.NodeKey,
			Type:       n.Type,
			Name:       n.Name,
			Config:     n.Config,
			Position:   n.Position,
		})
		if err != nil {
			return nil, fmt.Errorf("copy node %q: %w", n.NodeKey, err)
		}
		idByKey[n.NodeKey] = row.ID
	}
	keyByID := map[string]string{}
	for _, n := range src.Nodes {
		keyByID[util.UUIDToString(n.ID)] = n.NodeKey
	}
	for _, e := range src.Edges {
		fromKey, toKey := keyByID[util.UUIDToString(e.FromNodeID)], keyByID[util.UUIDToString(e.ToNodeID)]
		if _, err := qtx.CreateWorkflowEdge(ctx, db.CreateWorkflowEdgeParams{
			TemplateID: fresh.ID,
			FromNodeID: idByKey[fromKey],
			ToNodeID:   idByKey[toKey],
			Priority:   e.Priority,
			Condition:  nil,
		}); err != nil {
			return nil, fmt.Errorf("copy edge %q → %q: %w", fromKey, toKey, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return s.GetTemplate(ctx, workspaceID, fresh.ID)
}

// PublishTemplate freezes a draft: resolves every agent node's selector to a
// concrete agent UUID (written back into the node config so runtime never
// re-resolves, D-7), validates that evaluator-role nodes do not share an
// agent with any upstream executor node, then flips draft → published under
// the guarded status UPDATE.
func (s *TemplateService) PublishTemplate(ctx context.Context, workspaceID, templateID pgtype.UUID) (*TemplateDetail, error) {
	detail, err := s.GetTemplate(ctx, workspaceID, templateID)
	if err != nil {
		return nil, err
	}
	if detail.Template.Status != "draft" {
		return nil, ErrTemplateNotDraft
	}

	nodeInputs := make([]NodeInput, 0, len(detail.Nodes))
	edgeInputs := make([]EdgeInput, 0, len(detail.Edges))
	keyByID := map[string]string{}
	configByKey := map[string]NodeConfig{}
	for _, n := range detail.Nodes {
		keyByID[util.UUIDToString(n.ID)] = n.NodeKey
	}
	for _, n := range detail.Nodes {
		cfg, err := ParseNodeConfig(n.Config)
		if err != nil {
			return nil, fmt.Errorf("node %q: %w", n.NodeKey, err)
		}
		configByKey[n.NodeKey] = cfg
		nodeInputs = append(nodeInputs, NodeInput{NodeKey: n.NodeKey, Type: n.Type, Name: n.Name, Config: n.Config})
	}
	for _, e := range detail.Edges {
		edgeInputs = append(edgeInputs, EdgeInput{
			FromNodeKey: keyByID[util.UUIDToString(e.FromNodeID)],
			ToNodeKey:   keyByID[util.UUIDToString(e.ToNodeID)],
			Priority:    e.Priority,
		})
	}
	if err := validateTemplateGraph(nodeInputs, edgeInputs); err != nil {
		return nil, err
	}

	// Build a snapshot view for upstream queries, then resolve selectors.
	snap := &Snapshot{Nodes: []SnapshotNode{}, Edges: []SnapshotEdge{}}
	for _, n := range detail.Nodes {
		snap.Nodes = append(snap.Nodes, SnapshotNode{NodeKey: n.NodeKey, Type: n.Type, Name: n.Name, Config: configByKey[n.NodeKey]})
	}
	for _, e := range edgeInputs {
		snap.Edges = append(snap.Edges, SnapshotEdge{FromNodeKey: e.FromNodeKey, ToNodeKey: e.ToNodeKey, Priority: e.Priority})
	}

	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := s.Queries.WithTx(tx)

	for i := range detail.Nodes {
		n := &detail.Nodes[i]
		if n.Type != NodeTypeAgent {
			continue
		}
		cfg := configByKey[n.NodeKey]
		selector := cfg.AgentSelector
		if selector == "" {
			selector = cfg.AgentID
		}
		agent, err := s.resolveAgent(ctx, workspaceID, selector)
		if err != nil {
			return nil, fmt.Errorf("node %q: %w", n.NodeKey, err)
		}
		cfg.AgentID = util.UUIDToString(agent.ID)
		configByKey[n.NodeKey] = cfg
		frozen, err := json.Marshal(cfg)
		if err != nil {
			return nil, fmt.Errorf("node %q: marshal config: %w", n.NodeKey, err)
		}
		if _, err := qtx.UpdateWorkflowNode(ctx, db.UpdateWorkflowNodeParams{
			ID:     n.ID,
			Config: frozen,
		}); err != nil {
			return nil, fmt.Errorf("freeze node %q config: %w", n.NodeKey, err)
		}
	}

	// Produce/review separation: an evaluator must not be the same agent as
	// any upstream executor (blueprint pillar 5, design.md §4.3).
	for _, n := range snap.Nodes {
		if n.Type != NodeTypeAgent || n.Config.EffectiveRole() != RoleEvaluator {
			continue
		}
		evaluatorAgent := configByKey[n.NodeKey].AgentID
		for _, upKey := range snap.UpstreamNodeKeys(n.NodeKey) {
			up := snap.NodeByKey(upKey)
			if up == nil || up.Type != NodeTypeAgent || up.Config.EffectiveRole() != RoleExecutor {
				continue
			}
			if configByKey[upKey].AgentID == evaluatorAgent {
				return nil, &EvaluatorSeparationError{NodeKey: n.NodeKey, UpstreamKey: upKey, AgentID: evaluatorAgent}
			}
		}
	}

	// Publishing vN+1 retires the currently published version of the key
	// (idx_workflow_template_one_published enforces one published row per
	// key). Same tx as the status flip so a crash never leaves zero or two
	// published versions.
	if err := qtx.ArchivePublishedWorkflowTemplateByKey(ctx, db.ArchivePublishedWorkflowTemplateByKeyParams{
		WorkspaceID: workspaceID, Key: detail.Template.Key,
	}); err != nil {
		return nil, fmt.Errorf("archive previous published version: %w", err)
	}
	if _, err := qtx.UpdateWorkflowTemplateStatus(ctx, db.UpdateWorkflowTemplateStatusParams{
		NewStatus: "published", ID: templateID, ExpectedStatus: "draft",
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrTemplateConflict
		}
		return nil, fmt.Errorf("publish template: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return s.GetTemplate(ctx, workspaceID, templateID)
}

// resolveAgent maps an agent_selector to exactly one workspace agent. A
// selector that parses as a UUID resolves by ID (no name fallback — a
// UUID-shaped name would silently re-target); anything else matches
// agent.name exactly and must be unique.
func (s *TemplateService) resolveAgent(ctx context.Context, workspaceID pgtype.UUID, selector string) (db.Agent, error) {
	if selector == "" {
		return db.Agent{}, ErrAgentNotFound
	}
	if id, err := util.ParseUUID(selector); err == nil {
		agent, err := s.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{ID: id, WorkspaceID: workspaceID})
		if err != nil {
			return db.Agent{}, ErrAgentNotFound
		}
		if agent.ArchivedAt.Valid {
			return db.Agent{}, ErrAgentNotFound
		}
		return agent, nil
	}
	agents, err := s.Queries.ListAgents(ctx, workspaceID)
	if err != nil {
		return db.Agent{}, fmt.Errorf("list agents: %w", err)
	}
	var matches []db.Agent
	for _, a := range agents {
		if a.Name == selector && !a.ArchivedAt.Valid {
			matches = append(matches, a)
		}
	}
	switch len(matches) {
	case 0:
		return db.Agent{}, ErrAgentNotFound
	case 1:
		return matches[0], nil
	default:
		return db.Agent{}, ErrAgentAmbiguous
	}
}

// BuildSnapshot assembles the frozen run snapshot from a published
// template's immutable rows (selector→UUID freezing already happened at
// publish, so the snapshot carries concrete agent IDs).
func BuildSnapshot(ctx context.Context, q *db.Queries, tmpl db.WorkflowTemplate) (*Snapshot, error) {
	nodes, err := q.ListWorkflowNodes(ctx, tmpl.ID)
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	edges, err := q.ListWorkflowEdgesForTemplate(ctx, tmpl.ID)
	if err != nil {
		return nil, fmt.Errorf("list edges: %w", err)
	}
	idToKey := map[string]string{}
	for _, n := range nodes {
		idToKey[util.UUIDToString(n.ID)] = n.NodeKey
	}
	snap := &Snapshot{
		TemplateID: util.UUIDToString(tmpl.ID),
		Key:        tmpl.Key,
		Version:    tmpl.Version,
		Nodes:      make([]SnapshotNode, 0, len(nodes)),
		Edges:      make([]SnapshotEdge, 0, len(edges)),
	}
	for _, n := range nodes {
		cfg, err := ParseNodeConfig(n.Config)
		if err != nil {
			return nil, fmt.Errorf("node %q: %w", n.NodeKey, err)
		}
		snap.Nodes = append(snap.Nodes, SnapshotNode{
			NodeKey: n.NodeKey,
			Type:    n.Type,
			Name:    n.Name,
			Config:  cfg,
		})
	}
	for _, e := range edges {
		snap.Edges = append(snap.Edges, SnapshotEdge{
			FromNodeKey: idToKey[util.UUIDToString(e.FromNodeID)],
			ToNodeKey:   idToKey[util.UUIDToString(e.ToNodeID)],
			Priority:    e.Priority,
		})
	}
	return snap, nil
}

// ---------------------------------------------------------------------------
// Exit-fields validation (dual-layer: handler + RecordSubmission both call)
// ---------------------------------------------------------------------------

// FieldError is one structured validation failure. Code is "missing" for an
// absent required field and "type_mismatch" for a present field of the wrong
// JSON type. Unknown submitted fields never produce errors (D-9).
type FieldError struct {
	Name     string `json:"name"`
	Code     string `json:"code"`
	Expected string `json:"expected,omitempty"`
	Message  string `json:"message"`
}

// ExitFieldsValidationError is the 422-shaped validation failure.
type ExitFieldsValidationError struct {
	Fields []FieldError
}

func (e *ExitFieldsValidationError) Error() string {
	return fmt.Sprintf("workflow: exit_fields validation failed (%d field errors)", len(e.Fields))
}

// ValidateExitFields checks submitted exit_fields against the node schema.
// Semantics (design.md §1 D-9): missing required = structured error; missing
// optional = fine; unknown fields = tolerated passthrough; type mismatch on
// a declared field = structured error.
func ValidateExitFields(schema *ExitFieldsSchema, fields map[string]any) []FieldError {
	if schema == nil {
		return nil
	}
	var errs []FieldError
	for _, spec := range schema.Fields {
		v, present := fields[spec.Name]
		if !present || v == nil {
			if spec.Required {
				errs = append(errs, FieldError{
					Name:     spec.Name,
					Code:     "missing",
					Expected: spec.Type,
					Message:  fmt.Sprintf("required exit field %q is missing", spec.Name),
				})
			}
			continue
		}
		if spec.Type == "" || spec.Type == "any" {
			continue
		}
		if !jsonTypeMatches(spec.Type, v) {
			errs = append(errs, FieldError{
				Name:     spec.Name,
				Code:     "type_mismatch",
				Expected: spec.Type,
				Message:  fmt.Sprintf("exit field %q must be of type %s", spec.Name, spec.Type),
			})
		}
	}
	return errs
}

// ValidateExitFieldsForStatus applies the status-aware 准出 rule: a
// completion claim (DONE / DONE_WITH_CONCERNS) must satisfy required fields;
// a blocked signal (BLOCKED / NEEDS_CONTEXT) cannot produce them by
// definition, so only provided fields are type-checked — requiring them
// would deadlock the run (the agent could never report being blocked).
func ValidateExitFieldsForStatus(status string, schema *ExitFieldsSchema, fields map[string]any) []FieldError {
	errs := ValidateExitFields(schema, fields)
	if status == SubmissionDone || status == SubmissionDoneWithConcerns {
		return errs
	}
	out := errs[:0]
	for _, e := range errs {
		if e.Code != "missing" {
			out = append(out, e)
		}
	}
	return out
}

// jsonTypeMatches reports whether a decoded JSON value matches the schema
// type name. Numbers accept both float64 (encoding/json default) and
// json.Number (decoder with UseNumber).
func jsonTypeMatches(want string, v any) bool {
	switch want {
	case "string":
		_, ok := v.(string)
		return ok
	case "number":
		switch v.(type) {
		case float64, float32, int, int64, int32, json.Number:
			return true
		}
		return false
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "object":
		_, ok := v.(map[string]any)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	default:
		return true
	}
}

// ---------------------------------------------------------------------------
// Artifacts validation (D-11; dual-layer like exit fields)
// ---------------------------------------------------------------------------

// ArtifactsValidationError is the structured D-11 rejection: one FieldError
// per offending string, named by its JSON path inside the artifacts blob.
type ArtifactsValidationError struct {
	Fields []FieldError
}

func (e *ArtifactsValidationError) Error() string {
	return fmt.Sprintf("workflow: artifacts validation failed (%d local path references)", len(e.Fields))
}

// ValidateArtifacts enforces D-11 (design.md §1, inventory 6.2): artifacts
// carry only durable references — PR URLs, branch names, attachment IDs.
// Local filesystem paths are rejected because the daemon workdir is garbage
// collected within 24h, which would silently void the evidence trail. The
// blob may be any JSON shape; every string scalar is checked.
func ValidateArtifacts(raw json.RawMessage) []FieldError {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return []FieldError{{
			Name:    "artifacts",
			Code:    "invalid_json",
			Message: "artifacts must be valid JSON",
		}}
	}
	var errs []FieldError
	walkArtifacts(v, "artifacts", &errs)
	return errs
}

// walkArtifacts recurses objects/arrays and flags path-shaped strings.
func walkArtifacts(v any, path string, errs *[]FieldError) {
	switch t := v.(type) {
	case map[string]any:
		for k, sub := range t {
			walkArtifacts(sub, path+"."+k, errs)
		}
	case []any:
		for i, sub := range t {
			walkArtifacts(sub, fmt.Sprintf("%s[%d]", path, i), errs)
		}
	case string:
		if looksLikeFilesystemPath(t) {
			*errs = append(*errs, FieldError{
				Name:    path,
				Code:    "local_path",
				Message: fmt.Sprintf("artifact %q references a local filesystem path; use durable references (PR URL, branch, attachment ID) — workdir paths are garbage collected (D-11)", path),
			})
		}
	}
}

// looksLikeFilesystemPath reports whether a string is shaped like a local
// filesystem reference rather than a durable artifact reference.
func looksLikeFilesystemPath(s string) bool {
	if s == "" {
		return false
	}
	// Absolute POSIX or home-relative.
	if strings.HasPrefix(s, "/") || strings.HasPrefix(s, "~/") {
		return true
	}
	// Relative POSIX.
	if s == "." || s == ".." || strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../") {
		return true
	}
	// Windows absolute (C:\ or C:/) or relative (.\ or ..\).
	if len(s) >= 3 && isASCIILetter(s[0]) && s[1] == ':' && (s[2] == '\\' || s[2] == '/') {
		return true
	}
	if strings.HasPrefix(s, `.\`) || strings.HasPrefix(s, `..\`) {
		return true
	}
	// file:// URLs are local paths in disguise.
	if strings.HasPrefix(s, "file://") {
		return true
	}
	// The daemon workdir layout itself (D-11): a "workdir" path segment.
	// Requires a separator so a branch plainly named "workdir" stays legal.
	if strings.ContainsAny(s, `/\`) {
		for _, seg := range strings.FieldsFunc(s, func(r rune) bool { return r == '/' || r == '\\' }) {
			if seg == "workdir" {
				return true
			}
		}
	}
	return false
}

func isASCIILetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}
