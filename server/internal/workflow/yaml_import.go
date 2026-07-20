// yaml_import.go — P1-8 harness YAML import tool (PRD P1-8). Parses the
// harness pipeline YAML schema (harness/pipeline/{standard,bugfix}.yaml)
// and converts it into CreateTemplateParams, ready for TemplateService.
// CreateTemplate. Pure library functions: no I/O, no DB — the caller
// (CLI command, test, or future HTTP endpoint) drives persistence.
//
// Mapping rules (PRD R2/R3/R4):
//   - role=planner     → NodeTypeAgent{role=executor}
//   - role=implementer → NodeTypeAgent{role=executor}; if stage.gate.type
//     =script, ALSO emit a NodeTypeGate after the agent (split: agent +
//     gate). This preserves the bugfix.yaml "Implement + Baseline" shape
//     (agent + hard baseline gate) without forcing the YAML author to
//     declare two stages.
//   - role=reviewer    → NodeTypeAgent{role=evaluator}. A reviewer stage
//     may carry gate.type=soft as advisory metadata; soft gates do NOT
//     split (the reviewer IS the human-equivalent soft gate).
//   - role=gate-runner → NodeTypeGate (always; gate.* fields populate
//     GateType/GateScriptRef/GateOnFail).
//   - role=human       → NodeTypeAcceptance.
//   - human_gates with after_stage=K (K != max stage id) → insert a
//     NodeTypeAcceptance after stage K's last emitted node.
//   - human_gates with at_stage=K → informational only (corresponds to
//     the role=human stage at K, which already emits an acceptance node).
//   - Append NodeTypeEnd at the end of the chain.
//   - Linear edges: chain[i] → chain[i+1] with priority 0 and nil
//     condition (P0/P1-1 catch-all semantics).
//   - produces:[a,b,c] → single exit field {name:"artifacts", type:"array",
//     description:"a, b, c"} (PRD R4 simplification — single-field
//     artifacts list rather than per-file fields).
//   - gate.hard=true → gate_on_fail="block"; gate.hard=false → "warn".
//   - gate.script    → gate_script_ref (passed through verbatim; may
//     include args like "baseline.py diff").
//
// The conversion leaves AgentSelector empty on agent/gate nodes — agent
// binding is a workspace-specific concern that the caller layers in
// before publish (mirrors the seed template + CLI --*-agent flow).
package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// YAML schema types
// ---------------------------------------------------------------------------

// PipelineYAML is the parsed harness pipeline YAML root.
type PipelineYAML struct {
	Pipeline PipelineYAMLRoot `yaml:"pipeline"`
}

// PipelineYAMLRoot carries the top-level pipeline fields.
type PipelineYAMLRoot struct {
	Name           string                  `yaml:"name"`
	Description    string                  `yaml:"description"`
	Stages         []PipelineStage         `yaml:"stages"`
	HumanGates     []PipelineHumanGate     `yaml:"human_gates"`
	CircuitBreaker *PipelineCircuitBreaker `yaml:"circuit_breaker"`
}

// PipelineStage is one stage entry in the YAML's stages list.
type PipelineStage struct {
	ID            int           `yaml:"id"`
	Name          string        `yaml:"name"`
	Role          string        `yaml:"role"`
	InitialStatus string        `yaml:"initial_status"`
	Produces      []string      `yaml:"produces"`
	Gate          *PipelineGate `yaml:"gate"`
	AutoPass      bool          `yaml:"auto_pass"`
}

// PipelineGate is the per-stage gate config block.
type PipelineGate struct {
	Type   string `yaml:"type"`
	Script string `yaml:"script"`
	Hard   *bool  `yaml:"hard"`
	OnFail string `yaml:"on_fail"`
}

// PipelineHumanGate is one entry in the YAML's human_gates list.
// Exactly one of AfterStage / AtStage is set per entry.
type PipelineHumanGate struct {
	AfterStage        *int   `yaml:"after_stage"`
	AtStage           *int   `yaml:"at_stage"`
	Name              string `yaml:"name"`
	Description       string `yaml:"description"`
	AutoPassCondition string `yaml:"auto_pass_condition"`
}

// PipelineCircuitBreaker is parsed but unused by the conversion (P0
// engine does not yet carry circuit_breaker metadata on the template;
// the harness layer enforces it out-of-band).
type PipelineCircuitBreaker struct {
	Threshold int    `yaml:"threshold"`
	Action    string `yaml:"action"`
}

// Default agent selector names used when the YAML itself does not name
// agents (which is the common case — harness YAML is agent-agnostic).
// These match the seed-template defaults so a workspace that already
// ran `multica workflow seed` has resolvable bindings out of the box.
// Operators with different agent names override via the CLI --*-agent
// flags (which feed ImportYAMLParams.AgentSelectors) or rebind via the
// template API after import.
const (
	DefaultImportPlannerAgent     = "workflow-planner"
	DefaultImportImplementerAgent = "workflow-implementer"
	DefaultImportReviewerAgent    = "workflow-reviewer"
)

// ImportAgentSelectors overrides the default agent selector names. Empty
// fields fall back to the DefaultImport* constants. Used by the CLI to
// honor --planner-agent / --implementer-agent / --reviewer-agent flags.
type ImportAgentSelectors struct {
	Planner     string
	Implementer string
	Reviewer    string
}

func (s ImportAgentSelectors) withDefaults() ImportAgentSelectors {
	if s.Planner == "" {
		s.Planner = DefaultImportPlannerAgent
	}
	if s.Implementer == "" {
		s.Implementer = DefaultImportImplementerAgent
	}
	if s.Reviewer == "" {
		s.Reviewer = DefaultImportReviewerAgent
	}
	return s
}

// acceptedStageRoles is the whitelist of YAML stage.role values.
var acceptedStageRoles = map[string]string{
	"planner":     RoleExecutor,
	"implementer": RoleExecutor,
	"reviewer":    RoleEvaluator,
	"gate-runner": "", // emitted as NodeTypeGate, not agent
	"human":       "", // emitted as NodeTypeAcceptance
}

// importNode is the converter's working unit: one emitted node's key +
// type + display name + parsed config. The chain is built as a slice
// of these, then frozen into NodeInput + EdgeInput at the end.
type importNode struct {
	key    string
	typ    string
	name   string
	config NodeConfig
}

// ---------------------------------------------------------------------------
// Parse
// ---------------------------------------------------------------------------

// ParsePipelineYAML decodes the harness YAML schema and validates the
// stage list. Returns a struct ready for ConvertYAMLToTemplate.
func ParsePipelineYAML(raw []byte) (*PipelineYAML, error) {
	var p PipelineYAML
	if err := yaml.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("workflow: parse pipeline yaml: %w", err)
	}
	if err := validatePipelineYAML(&p); err != nil {
		return nil, err
	}
	return &p, nil
}

// validatePipelineYAML enforces the schema invariants the converter
// relies on: pipeline.name present, ≥1 stage, unique non-zero stage ids,
// every stage has a name + recognized role.
func validatePipelineYAML(p *PipelineYAML) error {
	if p == nil {
		return fmt.Errorf("workflow: pipeline yaml is nil")
	}
	if p.Pipeline.Name == "" {
		return fmt.Errorf("workflow: pipeline.name is required")
	}
	if len(p.Pipeline.Stages) == 0 {
		return fmt.Errorf("workflow: pipeline.stages is empty")
	}
	seenIDs := map[int]bool{}
	for i := range p.Pipeline.Stages {
		s := &p.Pipeline.Stages[i]
		if s.ID == 0 {
			return fmt.Errorf("workflow: stages[%d].id is required (must be > 0)", i)
		}
		if seenIDs[s.ID] {
			return fmt.Errorf("workflow: duplicate stage id %d", s.ID)
		}
		seenIDs[s.ID] = true
		if s.Name == "" {
			return fmt.Errorf("workflow: stage %d requires a name", s.ID)
		}
		if _, ok := acceptedStageRoles[s.Role]; !ok {
			return fmt.Errorf("workflow: stage %d (%q) has unknown role %q", s.ID, s.Name, s.Role)
		}
	}
	// human_gate anchor validation: every after_stage/at_stage must name
	// a real stage id. Without this, the converter would silently drop
	// the human_gate (and the operator would not notice the missing
	// acceptance node).
	stageIDs := map[int]bool{}
	for _, s := range p.Pipeline.Stages {
		stageIDs[s.ID] = true
	}
	for i, hg := range p.Pipeline.HumanGates {
		anchor := 0
		switch {
		case hg.AfterStage != nil && hg.AtStage != nil:
			return fmt.Errorf("workflow: human_gates[%d] sets both after_stage and at_stage", i)
		case hg.AfterStage != nil:
			anchor = *hg.AfterStage
		case hg.AtStage != nil:
			anchor = *hg.AtStage
		default:
			return fmt.Errorf("workflow: human_gates[%d] (%q) must set after_stage or at_stage", i, hg.Name)
		}
		if !stageIDs[anchor] {
			return fmt.Errorf("workflow: human_gates[%d] (%q) references unknown stage id %d", i, hg.Name, anchor)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Convert
// ---------------------------------------------------------------------------

// ConvertYAMLToTemplate builds CreateTemplateParams from parsed YAML.
// The returned params have empty WorkspaceID/CreatedBy — the caller
// fills those before invoking TemplateService.CreateTemplate.
//
// key is the stable template key (workflow_template.key UNIQUE). If
// empty, pipeline.name is used.
//
// Agent nodes are bound to DefaultImport* selector names (matching the
// seed defaults) so the returned graph passes validateTemplateGraph
// without further work. Callers that need different agent names pass
// an ImportAgentSelectors via ConvertYAMLToTemplateWithSelectors.
func ConvertYAMLToTemplate(p *PipelineYAML, key string) (CreateTemplateParams, error) {
	return ConvertYAMLToTemplateWithSelectors(p, key, ImportAgentSelectors{})
}

// ConvertYAMLToTemplateWithSelectors is the selector-aware variant. Empty
// selector fields fall back to the DefaultImport* names.
func ConvertYAMLToTemplateWithSelectors(p *PipelineYAML, key string, sels ImportAgentSelectors) (CreateTemplateParams, error) {
	if p == nil {
		return CreateTemplateParams{}, fmt.Errorf("workflow: nil pipeline yaml")
	}
	if key == "" {
		key = p.Pipeline.Name
	}
	if key == "" {
		return CreateTemplateParams{}, fmt.Errorf("workflow: template key is required (pipeline.name is empty)")
	}
	sels = sels.withDefaults()

	var chain []importNode
	humanAfter := map[int][]PipelineHumanGate{}
	for _, hg := range p.Pipeline.HumanGates {
		if hg.AfterStage != nil {
			humanAfter[*hg.AfterStage] = append(humanAfter[*hg.AfterStage], hg)
		}
	}

	for _, s := range p.Pipeline.Stages {
		emit := convertStageToNodes(s, sels)
		for _, n := range emit {
			chain = append(chain, n)
		}
		// Insert any after_stage human gates immediately after this
		// stage's last node. Sorted by name for deterministic ordering
		// when multiple gates anchor on the same stage.
		if gates, ok := humanAfter[s.ID]; ok {
			sort.SliceStable(gates, func(i, j int) bool { return gates[i].Name < gates[j].Name })
			for _, hg := range gates {
				key := slugify(hg.Name)
				if key == "" {
					key = fmt.Sprintf("human-gate-stage-%d", s.ID)
				}
				// De-dupe against any existing chain key (rare: a
				// stage and its gate share a slug).
				key = uniqueKey(chain, key)
				chain = append(chain, importNode{
					key:  key,
					typ:  NodeTypeAcceptance,
					name: hg.Name,
					config: NodeConfig{
						Instructions: hg.Description,
					},
				})
			}
		}
	}

	// Append terminal end node.
	chain = append(chain, importNode{
		key:  uniqueKey(chain, "end"),
		typ:  NodeTypeEnd,
		name: "End",
	})

	// Build NodeInput slice (marshal configs to JSON) + linear edges.
	nodes := make([]NodeInput, 0, len(chain))
	for _, n := range chain {
		raw, err := json.Marshal(n.config)
		if err != nil {
			return CreateTemplateParams{}, fmt.Errorf("workflow: marshal node %q config: %w", n.key, err)
		}
		nodes = append(nodes, NodeInput{
			NodeKey: n.key,
			Type:    n.typ,
			Name:    n.name,
			Config:  raw,
		})
	}
	edges := make([]EdgeInput, 0, len(chain)-1)
	for i := 0; i+1 < len(chain); i++ {
		edges = append(edges, EdgeInput{
			FromNodeKey: chain[i].key,
			ToNodeKey:   chain[i+1].key,
		})
	}

	return CreateTemplateParams{
		Key:         key,
		Name:        p.Pipeline.Name,
		Description: p.Pipeline.Description,
		Nodes:       nodes,
		Edges:       edges,
	}, nil
}

// convertStageToNodes emits the node(s) for one YAML stage. Most stages
// produce one node; an implementer stage with gate.type=script produces
// two (agent + gate) to preserve the bugfix "Implement + Baseline" shape.
func convertStageToNodes(s PipelineStage, sels ImportAgentSelectors) []importNode {
	baseKey := slugify(s.Name)
	if baseKey == "" {
		baseKey = fmt.Sprintf("stage-%d", s.ID)
	}

	emitGate := func(nameSuffix, keySuffix string, g PipelineGate) importNode {
		gateKey := baseKey + keySuffix
		return importNode{
			key:  gateKey,
			typ:  NodeTypeGate,
			name: s.Name + nameSuffix,
			config: NodeConfig{
				GateType:      GateTypeScript,
				GateScriptRef: g.Script,
				GateOnFail:    gateOnFailFromHard(g.Hard),
			},
		}
	}

	switch s.Role {
	case "planner":
		return []importNode{{
			key:  baseKey,
			typ:  NodeTypeAgent,
			name: s.Name,
			config: NodeConfig{
				Role:          RoleExecutor,
				AgentSelector: sels.Planner,
				ExitFields:    producesToExitFields(s.Produces),
			},
		}}
	case "implementer":
		agentNode := importNode{
			key:  baseKey,
			typ:  NodeTypeAgent,
			name: s.Name,
			config: NodeConfig{
				Role:          RoleExecutor,
				AgentSelector: sels.Implementer,
				ExitFields:    producesToExitFields(s.Produces),
			},
		}
		// Split: non-gate-runner stage with gate.type=script emits a
		// separate gate node after the agent. bugfix.yaml stage 2
		// ("Implement + Baseline") relies on this to produce the
		// baseline gate without forcing the YAML to declare two stages.
		if s.Gate != nil && s.Gate.Type == "script" {
			return []importNode{agentNode, emitGate(" Gate", "-gate", *s.Gate)}
		}
		return []importNode{agentNode}
	case "reviewer":
		return []importNode{{
			key:  baseKey,
			typ:  NodeTypeAgent,
			name: s.Name,
			config: NodeConfig{
				Role:          RoleEvaluator,
				AgentSelector: sels.Reviewer,
				ExitFields:    producesToExitFields(s.Produces),
			},
		}}
	case "gate-runner":
		// role=gate-runner always emits a gate node. A missing gate
		// block is a YAML error — surface it as a gate node with an
		// empty script_ref, which validateTemplateGraph will reject at
		// CreateTemplate time with a clear message.
		var g PipelineGate
		if s.Gate != nil {
			g = *s.Gate
		}
		cfg := NodeConfig{
			GateOnFail: gateOnFailFromHard(g.Hard),
		}
		// All gate-runner forms (script / soft / unknown) map onto the
		// engine's NodeTypeGate with gate_type=script. A soft or
		// unknown type leaves GateScriptRef empty if the YAML omitted
		// it, which validateGateConfig rejects with a clear "must set
		// exactly one of gate_script_ref or gate_inline_script" error.
		cfg.GateType = GateTypeScript
		cfg.GateScriptRef = g.Script
		return []importNode{{
			key:    baseKey,
			typ:    NodeTypeGate,
			name:   s.Name,
			config: cfg,
		}}
	case "human":
		return []importNode{{
			key:    baseKey,
			typ:    NodeTypeAcceptance,
			name:   s.Name,
			config: NodeConfig{},
		}}
	}
	return nil
}

// producesToExitFields collapses a YAML produces list into the single
// "artifacts" exit field (PRD R4 simplification).
func producesToExitFields(produces []string) *ExitFieldsSchema {
	if len(produces) == 0 {
		return nil
	}
	return &ExitFieldsSchema{Fields: []ExitFieldSpec{{
		Name:        "artifacts",
		Type:        "array",
		Required:    true,
		Description: strings.Join(produces, ", "),
	}}}
}

// gateOnFailFromHard maps the YAML gate.hard boolean onto the engine's
// gate_on_fail enum. nil (unset) defaults to block (the engine default).
func gateOnFailFromHard(hard *bool) string {
	if hard == nil {
		return ""
	}
	if *hard {
		return GateOnFailBlock
	}
	return GateOnFailWarn
}

// slugify produces a kebab-case node_key from a free-form name. Used for
// YAML-imported stage names (which may contain spaces, punctuation,
// parens). The result is lowercase, alphanumeric + dashes only.
var slugNonAlnum = regexp.MustCompile(`[^a-z0-9]+`)
var slugTrimDash = regexp.MustCompile(`^-+|-+$`)

func slugify(name string) string {
	if name == "" {
		return ""
	}
	s := strings.ToLower(name)
	s = slugNonAlnum.ReplaceAllString(s, "-")
	s = slugTrimDash.ReplaceAllString(s, "")
	return s
}

// uniqueKey appends -2, -3, ... to base until it no longer collides with
// any key already in chain. The converter uses this for the terminal
// "end" node and any after_stage human gate whose slug collided with a
// stage slug.
func uniqueKey(chain []importNode, base string) string {
	seen := map[string]bool{}
	for _, c := range chain {
		seen[c.key] = true
	}
	if !seen[base] {
		return base
	}
	for i := 2; ; i++ {
		cand := fmt.Sprintf("%s-%d", base, i)
		if !seen[cand] {
			return cand
		}
	}
}

// ---------------------------------------------------------------------------
// Orchestrator (parse + convert + create)
// ---------------------------------------------------------------------------

// ImportYAMLParams carries the caller-supplied identifiers that the
// YAML itself does not provide.
type ImportYAMLParams struct {
	WorkspaceID string
	CreatedBy   string
	// Key overrides pipeline.name as the template key. Empty falls
	// back to pipeline.name (the YAML import default).
	Key string
	// Agents overrides the default agent selector names bound to
	// agent nodes. Empty fields fall back to DefaultImport*.
	Agents ImportAgentSelectors
}

// ImportYAMLFromBytes parses YAML, converts it, and creates the draft
// template in one call. This is the orchestrator the CLI invokes; tests
// call it directly against a TemplateService backed by the test DB.
//
// Returns the created TemplateDetail (with the full graph) so the
// caller can publish, bind agents, or report node count.
func ImportYAMLFromBytes(
	ctx context.Context,
	svc *TemplateService,
	raw []byte,
	params ImportYAMLParams,
) (*TemplateDetail, error) {
	if svc == nil {
		return nil, fmt.Errorf("workflow: ImportYAMLFromBytes requires a TemplateService")
	}
	parsed, err := ParsePipelineYAML(raw)
	if err != nil {
		return nil, err
	}
	conv, err := ConvertYAMLToTemplateWithSelectors(parsed, params.Key, params.Agents)
	if err != nil {
		return nil, err
	}
	wsID, err := parseUUIDLoose(params.WorkspaceID)
	if err != nil {
		return nil, fmt.Errorf("workflow: import workspace_id: %w", err)
	}
	creator, err := parseUUIDLoose(params.CreatedBy)
	if err != nil {
		return nil, fmt.Errorf("workflow: import created_by: %w", err)
	}
	conv.WorkspaceID = wsID
	conv.CreatedBy = creator
	detail, err := svc.CreateTemplate(ctx, conv)
	if err != nil {
		return nil, err
	}
	return detail, nil
}

// parseUUIDLoose accepts both raw UUIDs and the empty string (empty →
// zero UUID, which CreateTemplate tolerates on WorkspaceID/CreatedBy
// when the caller is a system-level actor).
func parseUUIDLoose(s string) (pgtype.UUID, error) {
	if s == "" {
		return pgtype.UUID{}, nil
	}
	var u pgtype.UUID
	if err := u.Scan(s); err != nil {
		return pgtype.UUID{}, fmt.Errorf("parse uuid %q: %w", s, err)
	}
	return u, nil
}
