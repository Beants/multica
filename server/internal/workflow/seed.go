package workflow

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
)

// seed.go — the two P0 seed templates (R8, design.md §2): standard (9 nodes,
// mirroring the harness standard pipeline's 7 stages + Spec Freeze human
// gate + independent end) and bugfix (6 nodes, default-manual acceptance per
// the D-12 ruling). Gate/review stages map to evaluator-role agent nodes in
// P0 (deviation #4: the gate node type arrives in P1).
//
// Seeding is EXPLICIT (the `multica workflow seed` CLI → POST
// /api/workflow-templates/seed), never implicit on a read path: auto-seeding
// on first template list would turn an idempotent GET into a surprising
// write in workspaces that never asked for workflow templates.
//
// Seeding is idempotent per key: a key that already exists in the workspace
// (any status) is skipped, not duplicated. Agent bindings are configurable
// at seed time — publish resolves the selectors once and freezes UUIDs into
// the node configs (D-7), and the evaluator≠upstream-executor separation is
// preflight-checked here so a bad binding fails BEFORE any row is written.

// Seed template keys (stable external identifiers; hook payloads reference
// them via template_key).
const (
	SeedTemplateKeyStandard = "standard"
	SeedTemplateKeyBugfix   = "bugfix"
)

// Default seed agent selectors (agent NAMES — publish resolves them to
// UUIDs). Distinct per role so the produce/review separation rule holds out
// of the box; callers override per workspace via the seed request.
const (
	DefaultSeedPlannerAgent     = "workflow-planner"
	DefaultSeedImplementerAgent = "workflow-implementer"
	DefaultSeedGateAgent        = "workflow-gate-runner"
	DefaultSeedReviewAgent      = "workflow-reviewer"
)

// SeedAgentSelectors binds the four seed roles to workspace agents (name or
// UUID). GateRunner judges every gate stage; Reviewer judges the review
// stage. Gate/review agents must differ from both executor agents (publish
// validation, blueprint pillar 5); two evaluators MAY share an agent.
type SeedAgentSelectors struct {
	Planner     string `json:"planner_agent,omitempty"`
	Implementer string `json:"implementer_agent,omitempty"`
	GateRunner  string `json:"gate_agent,omitempty"`
	Reviewer    string `json:"review_agent,omitempty"`
}

// withDefaults fills unset selectors with the placeholder names.
func (s SeedAgentSelectors) withDefaults() SeedAgentSelectors {
	if s.Planner == "" {
		s.Planner = DefaultSeedPlannerAgent
	}
	if s.Implementer == "" {
		s.Implementer = DefaultSeedImplementerAgent
	}
	if s.GateRunner == "" {
		s.GateRunner = DefaultSeedGateAgent
	}
	if s.Reviewer == "" {
		s.Reviewer = DefaultSeedReviewAgent
	}
	return s
}

// SeedResult reports the per-template outcome of one seed call.
type SeedResult struct {
	Key        string `json:"key"`
	TemplateID string `json:"template_id,omitempty"`
	Version    int32  `json:"version,omitempty"`
	// Seeded is false when the key already existed in the workspace (the
	// idempotent skip) — the existing template is left untouched.
	Seeded bool `json:"seeded"`
}

// SeedTemplatesParams carries one seed invocation.
type SeedTemplatesParams struct {
	WorkspaceID pgtype.UUID
	CreatedBy   pgtype.UUID
	Selectors   SeedAgentSelectors
}

// SeedTemplates creates + publishes both seed templates in one call,
// skipping keys the workspace already has. Agent selectors are resolved
// (and the evaluator/executor separation rule checked) BEFORE any write, so
// an unresolvable binding fails cleanly instead of leaving a broken draft.
func (s *TemplateService) SeedTemplates(ctx context.Context, p SeedTemplatesParams) ([]SeedResult, error) {
	selectors := p.Selectors.withDefaults()

	existing := map[string]bool{}
	templates, err := s.ListTemplates(ctx, p.WorkspaceID)
	if err != nil {
		return nil, fmt.Errorf("list templates for seed idempotency: %w", err)
	}
	for _, t := range templates {
		existing[t.Key] = true
	}

	defs := seedTemplateDefs()
	want := []string{SeedTemplateKeyStandard, SeedTemplateKeyBugfix}
	results := make([]SeedResult, 0, len(want))
	for _, key := range want {
		def := defs[key]
		if existing[key] {
			results = append(results, SeedResult{Key: key, Seeded: false})
			continue
		}
		// Preflight: resolve every role's selector and check separation before
		// writing anything (a publish failure here would orphan a draft).
		bindings, err := s.preflightSeedAgents(ctx, p.WorkspaceID, key, selectors)
		if err != nil {
			return nil, err
		}
		detail, err := s.CreateTemplate(ctx, CreateTemplateParams{
			WorkspaceID: p.WorkspaceID,
			Key:         key,
			Name:        def.name,
			Description: def.description,
			CreatedBy:   p.CreatedBy,
			Nodes:       def.nodes(bindings),
			Edges:       def.edges(),
		})
		if err != nil {
			if isUniqueViolation(err) {
				// Lost a concurrent-seed race: the other caller's template now
				// holds the key — treat as the idempotent skip.
				results = append(results, SeedResult{Key: key, Seeded: false})
				continue
			}
			return nil, fmt.Errorf("seed %q: %w", key, err)
		}
		published, err := s.PublishTemplate(ctx, p.WorkspaceID, detail.Template.ID)
		if err != nil {
			return nil, fmt.Errorf("seed %q publish: %w", key, err)
		}
		results = append(results, SeedResult{
			Key:        key,
			TemplateID: util.UUIDToString(published.Template.ID),
			Version:    published.Template.Version,
			Seeded:     true,
		})
	}
	return results, nil
}

// seedAgentBindings maps each seed role to its resolved agent UUID string.
type seedAgentBindings struct {
	Planner     string
	Implementer string
	GateRunner  string
	Reviewer    string
}

// preflightSeedAgents resolves the four role selectors and enforces the
// evaluator≠upstream-executor rule against the RESOLVED ids (two different
// names can resolve to the same agent — publish would reject it later,
// after a draft row already exists).
func (s *TemplateService) preflightSeedAgents(ctx context.Context, workspaceID pgtype.UUID, key string, selectors SeedAgentSelectors) (seedAgentBindings, error) {
	resolve := func(role, selector string) (string, error) {
		agent, err := s.resolveAgent(ctx, workspaceID, selector)
		if err != nil {
			return "", fmt.Errorf("seed %q: %s agent selector %q: %w", key, role, selector, err)
		}
		return util.UUIDToString(agent.ID), nil
	}
	var b seedAgentBindings
	var err error
	if b.Planner, err = resolve("planner", selectors.Planner); err != nil {
		return b, err
	}
	if b.Implementer, err = resolve("implementer", selectors.Implementer); err != nil {
		return b, err
	}
	if b.GateRunner, err = resolve("gate-runner", selectors.GateRunner); err != nil {
		return b, err
	}
	if b.Reviewer, err = resolve("reviewer", selectors.Reviewer); err != nil {
		return b, err
	}
	executors := map[string]string{"planner": b.Planner, "implementer": b.Implementer}
	for evalRole, evalID := range map[string]string{"gate-runner": b.GateRunner, "reviewer": b.Reviewer} {
		for execRole, execID := range executors {
			if evalID == execID {
				return b, &EvaluatorSeparationError{
					NodeKey:     fmt.Sprintf("%s(%s)", key, evalRole),
					UpstreamKey: execRole,
					AgentID:     evalID,
				}
			}
		}
	}
	return b, nil
}

// ---------------------------------------------------------------------------
// Seed graph definitions
// ---------------------------------------------------------------------------

// seedTemplateDef is one seed template's static shape; nodes() instantiates
// it with the resolved agent bindings.
type seedTemplateDef struct {
	name        string
	description string
	chain       []string // node keys in chain order
	nodeSpecs   map[string]seedNodeSpec
}

// seedNodeSpec is one node's static config. agent names the seed role
// binding ("planner"/"implementer"/"gate"/"reviewer") an agent node uses;
// acceptance/end nodes leave it empty.
type seedNodeSpec struct {
	typ          string
	name         string
	role         string // executor/evaluator; empty for acceptance/end
	agent        string
	instructions string
	exitFields   *ExitFieldsSchema
	autoPass     bool
}

func seedTemplateDefs() map[string]seedTemplateDef {
	return map[string]seedTemplateDef{
		SeedTemplateKeyStandard: standardSeedDef(),
		SeedTemplateKeyBugfix:   bugfixSeedDef(),
	}
}

// nodes builds the service-layer node inputs, binding each agent node to
// the resolved agent UUID for its role. max_attempts=3 (P0 retry budget)
// and auto_pass=false (D-12) are explicit on every node.
func (d seedTemplateDef) nodes(b seedAgentBindings) []NodeInput {
	agentByRole := map[string]string{
		"planner":     b.Planner,
		"implementer": b.Implementer,
		"gate":        b.GateRunner,
		"reviewer":    b.Reviewer,
	}
	out := make([]NodeInput, 0, len(d.chain))
	for _, key := range d.chain {
		spec := d.nodeSpecs[key]
		cfg := NodeConfig{
			Role:         spec.role,
			Instructions: spec.instructions,
			ExitFields:   spec.exitFields,
			MaxAttempts:  defaultMaxAttempts,
			AutoPass:     spec.autoPass,
		}
		if spec.typ == NodeTypeAgent {
			cfg.AgentSelector = agentByRole[spec.agent]
		}
		raw, err := json.Marshal(cfg)
		if err != nil {
			// NodeConfig is fully static here; a marshal failure is a bug.
			panic(fmt.Sprintf("seed node %q config: %v", key, err))
		}
		out = append(out, NodeInput{NodeKey: key, Type: spec.typ, Name: spec.name, Config: raw})
	}
	return out
}

func (d seedTemplateDef) edges() []EdgeInput {
	edges := make([]EdgeInput, 0, len(d.chain)-1)
	for i := 0; i+1 < len(d.chain); i++ {
		edges = append(edges, EdgeInput{FromNodeKey: d.chain[i], ToNodeKey: d.chain[i+1]})
	}
	return edges
}

// standardSeedDef is the 9-node standard requirement chain (design.md §2,
// harness standard.yaml + squad-briefing.md):
// ①规划 → ②规划门禁 → ③Spec Freeze（人工）→ ④实现 → ⑤基线门禁 →
// ⑥API 门禁 → ⑦代码审查 → ⑧最终验收（人工）→ ⑨结束。
func standardSeedDef() seedTemplateDef {
	return seedTemplateDef{
		name:        "标准需求链路",
		description: "标准需求全流程（对应 harness 标准流水线 7 阶段 + Spec Freeze 人工关卡）：规划 → 规划门禁 → Spec Freeze → 实现 → 基线门禁 → API 门禁 → 代码审查 → 最终验收 → 完成。门禁/审查阶段为 evaluator 角色 agent 节点（P1 升级 gate 类型）。",
		chain:       []string{"plan", "plan-gate", "spec-freeze", "implement", "baseline-gate", "api-gate", "review", "final-acceptance", "done"},
		nodeSpecs: map[string]seedNodeSpec{
			"plan": {
				typ:   NodeTypeAgent,
				name:  "规划",
				role:  RoleExecutor,
				agent: "planner",
				instructions: "你是规划员（标准流水线阶段 1）。基于需求产出 prd、design、business-test-cases 三份文档：" +
					"prd 写清需求与验收标准，design 给出技术方案，business-test-cases 从需求直接推导（覆盖正常/边界/错误）。" +
					"只写文档：不写代码、不改 issue 状态。产物用持久引用（文档链接/附件 ID），禁止本地路径；" +
					"完成后经 `multica submission create` 提交准出字段 prd_url / design_url / business_test_cases_url。",
				exitFields: &ExitFieldsSchema{Fields: []ExitFieldSpec{
					{Name: "prd_url", Type: "string", Required: true, Description: "PRD 文档持久引用"},
					{Name: "design_url", Type: "string", Required: true, Description: "技术设计文档持久引用"},
					{Name: "business_test_cases_url", Type: "string", Required: true, Description: "业务测试用例文档持久引用"},
				}},
			},
			"plan-gate": {
				typ:   NodeTypeAgent,
				name:  "规划门禁",
				role:  RoleEvaluator,
				agent: "gate",
				instructions: "你是门禁执行器（阶段 2，硬门禁）。对上游规划产物执行规划契约检查（plan_contract_check 语义），" +
					"并冻结 before 基线快照（baseline snapshot --phase before --exclude api）。脚本输出的事实不可推翻：" +
					"只对失败逐条做处置判断（fatal 阻断 / flaky 重试 / 历史 warn）。独立审查，不受上游自声明影响；" +
					"经 `multica verdict create` 给出裁定（pass/fail/blocked），并随 verdict 提交 gate_report_url。",
				exitFields: &ExitFieldsSchema{Fields: []ExitFieldSpec{
					{Name: "gate_report_url", Type: "string", Required: true, Description: "规划门禁报告持久引用"},
					{Name: "baseline_before_url", Type: "string", Description: "冻结的 before 基线快照引用"},
				}},
			},
			"spec-freeze": {
				typ:  NodeTypeAcceptance,
				name: "Spec Freeze",
				instructions: "人工关卡（非自动阶段）：评审上游 prd 与 business-test-cases。通过后业务测试用例即冻结，下游不得修改；" +
					"驳回将定向返工到指定节点。",
			},
			"implement": {
				typ:   NodeTypeAgent,
				name:  "实现",
				role:  RoleExecutor,
				agent: "implementer",
				instructions: "你是实现员（阶段 3）。按已冻结的 prd/design 实现代码，补齐单元测试与技术测试用例（tech-test-cases）。" +
					"铁律：下游不可修改上游产物——要改文档就在评论里提，由验收/负责人打回上游重做。" +
					"完成后经 `multica submission create` 提交准出字段 pr_url / branch / summary（技术测试用例链接可选）。",
				exitFields: &ExitFieldsSchema{Fields: []ExitFieldSpec{
					{Name: "pr_url", Type: "string", Required: true, Description: "Pull Request 链接"},
					{Name: "branch", Type: "string", Required: true, Description: "实现分支名"},
					{Name: "summary", Type: "string", Required: true, Description: "实现摘要"},
					{Name: "tech_test_cases_url", Type: "string", Description: "技术测试用例文档持久引用"},
				}},
			},
			"baseline-gate": {
				typ:   NodeTypeAgent,
				name:  "基线门禁",
				role:  RoleEvaluator,
				agent: "gate",
				instructions: "你是门禁执行器（阶段 4，硬门禁）。执行基线 diff（baseline snapshot --phase after --exclude api + diff）：" +
					"只跑 unit/integration/lint/typecheck，只阻断新增失败（B−A）；历史失败记 warn 不阻断。" +
					"事实不可推翻，处置逐条判定。经 `multica verdict create` 给出裁定，并随 verdict 提交 baseline_diff_url。",
				exitFields: &ExitFieldsSchema{Fields: []ExitFieldSpec{
					{Name: "baseline_diff_url", Type: "string", Required: true, Description: "基线 diff 报告持久引用（仅新增失败 B−A）"},
				}},
			},
			"api-gate": {
				typ:   NodeTypeAgent,
				name:  "API 门禁",
				role:  RoleEvaluator,
				agent: "gate",
				instructions: "你是门禁执行器（阶段 5，硬门禁）。执行 API 接口 diff（api_gate snapshot --phase after + diff）：" +
					"只跑 test-plan 的 api 键；项目无 api 键则裁定 pass 并在 gate_report_url 的证据中注明 SKIP。" +
					"接口对不上是几秒能验证的事，不留到代码审查才暴露。经 `multica verdict create` 给出裁定。",
				exitFields: &ExitFieldsSchema{Fields: []ExitFieldSpec{
					{Name: "gate_report_url", Type: "string", Required: true, Description: "API 门禁报告持久引用（无 api 键时为 SKIP 说明）"},
				}},
			},
			"review": {
				typ:   NodeTypeAgent,
				name:  "代码审查",
				role:  RoleEvaluator,
				agent: "reviewer",
				instructions: "你是代码审查员（阶段 6，软门禁）。只评不改：读 diff 与技术测试用例，给出审查结论；" +
					"软门禁不阻断流程，问题暴露给最终验收。命名铁律：业务审查意见写准出字段 decision" +
					"（APPROVED / CONDITIONAL / REJECTED），流程裁定（pass/fail/blocked）经 `multica verdict create` 提交。",
				exitFields: &ExitFieldsSchema{Fields: []ExitFieldSpec{
					{Name: "decision", Type: "string", Required: true, Description: "审查结论：APPROVED / CONDITIONAL / REJECTED"},
					{Name: "review_report_url", Type: "string", Description: "审查报告（review-verdict）持久引用"},
				}},
			},
			"final-acceptance": {
				typ:  NodeTypeAcceptance,
				name: "最终验收",
				instructions: "人类验收（阶段 7）：验证全部业务测试用例通过——Agent completed ≠ business completed。" +
					"通过即交付；驳回指定返工节点并说明原因（下游已过门禁将重跑）。auto_pass 默认关闭（D-12）。",
			},
			"done": {
				typ:  NodeTypeEnd,
				name: "完成",
			},
		},
	}
}

// bugfixSeedDef is the 6-node bugfix short chain (design.md §2, harness
// bugfix.yaml): ①规划（精简）→ ②实现 → ③基线门禁 → ④代码审查 →
// ⑤验收（默认人工，auto_pass 默认关）→ ⑥结束。无 Spec Freeze、无独立
// API 门禁。
func bugfixSeedDef() seedTemplateDef {
	return seedTemplateDef{
		name:        "缺陷修复链路",
		description: "缺陷修复短链（对应 harness bugfix 流水线 4 阶段）：规划（精简）→ 实现 → 基线门禁 → 代码审查 → 人工验收 → 完成。无 Spec Freeze、无独立 API 门禁；验收默认人工（auto_pass 默认关闭，D-12）。",
		chain:       []string{"plan-lite", "implement", "baseline-gate", "review", "final-acceptance", "done"},
		nodeSpecs: map[string]seedNodeSpec{
			"plan-lite": {
				typ:   NodeTypeAgent,
				name:  "规划（精简）",
				role:  RoleExecutor,
				agent: "planner",
				instructions: "你是规划员（bugfix 精简阶段 1）。为缺陷修复产出精简 prd 与 business-test-cases（简单修复无需独立 design 文档）。" +
					"只写文档，不写代码。完成后经 `multica submission create` 提交准出字段 prd_url / business_test_cases_url。",
				exitFields: &ExitFieldsSchema{Fields: []ExitFieldSpec{
					{Name: "prd_url", Type: "string", Required: true, Description: "精简 PRD 文档持久引用"},
					{Name: "business_test_cases_url", Type: "string", Required: true, Description: "业务测试用例文档持久引用"},
					{Name: "design_url", Type: "string", Description: "技术设计文档持久引用（精简流程可选）"},
				}},
			},
			"implement": {
				typ:   NodeTypeAgent,
				name:  "实现",
				role:  RoleExecutor,
				agent: "implementer",
				instructions: "你是实现员（bugfix 阶段 2）。按 prd 修复缺陷并补回归测试与技术测试用例。" +
					"下游不可修改上游产物。完成后经 `multica submission create` 提交准出字段 pr_url / branch / summary。",
				exitFields: &ExitFieldsSchema{Fields: []ExitFieldSpec{
					{Name: "pr_url", Type: "string", Required: true, Description: "Pull Request 链接"},
					{Name: "branch", Type: "string", Required: true, Description: "修复分支名"},
					{Name: "summary", Type: "string", Required: true, Description: "修复摘要"},
					{Name: "tech_test_cases_url", Type: "string", Description: "技术测试用例文档持久引用"},
				}},
			},
			"baseline-gate": {
				typ:   NodeTypeAgent,
				name:  "基线门禁",
				role:  RoleEvaluator,
				agent: "gate",
				instructions: "你是门禁执行器（bugfix 阶段 3，硬门禁）。执行基线 diff（baseline snapshot --phase after + diff，" +
					"跑全部 test-plan 命令），只阻断新增失败。事实不可推翻，处置逐条判定。" +
					"经 `multica verdict create` 给出裁定，并随 verdict 提交 baseline_diff_url。",
				exitFields: &ExitFieldsSchema{Fields: []ExitFieldSpec{
					{Name: "baseline_diff_url", Type: "string", Required: true, Description: "基线 diff 报告持久引用（仅新增失败 B−A）"},
				}},
			},
			"review": {
				typ:   NodeTypeAgent,
				name:  "代码审查",
				role:  RoleEvaluator,
				agent: "reviewer",
				instructions: "你是代码审查员（bugfix 阶段 4，软门禁）。只评不改：读 diff 给出审查结论，软门禁不阻断。" +
					"业务审查意见写准出字段 decision（APPROVED / CONDITIONAL / REJECTED），" +
					"流程裁定经 `multica verdict create` 提交。",
				exitFields: &ExitFieldsSchema{Fields: []ExitFieldSpec{
					{Name: "decision", Type: "string", Required: true, Description: "审查结论：APPROVED / CONDITIONAL / REJECTED"},
					{Name: "review_report_url", Type: "string", Description: "审查报告（review-verdict）持久引用"},
				}},
			},
			"final-acceptance": {
				typ:  NodeTypeAcceptance,
				name: "验收",
				instructions: "人类验收（bugfix 默认人工，auto_pass 默认关闭——D-12 裁决）：验证缺陷修复与业务测试用例。" +
					"通过即交付；驳回指定返工节点并说明原因。",
			},
			"done": {
				typ:  NodeTypeEnd,
				name: "完成",
			},
		},
	}
}
