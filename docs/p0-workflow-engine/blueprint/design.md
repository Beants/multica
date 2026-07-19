# 蓝图 · 目标架构设计

> 平台 = 组织级 Harness。按六支柱划分子系统（决策 D2），in-repo 扩展 multica（决策 D3），内部团队工具优先（决策 D5）。
> 证据等级：实体/机制标注 `[已验证]`（带 file:line）/ `[推测]` / `[待业务确认]`。本文所有"新增表/新增包"均为设计草案，未实现。

## 1. 总体架构

```mermaid
flowchart TB
    subgraph EXT[外部系统]
        REQ[需求系统] BUG[缺陷系统] CRON[定时任务/历史问题池] OPS[负责人验收入口]
    end

    subgraph PLAT[Multica IDE 平台 · Go server in-repo 扩展]
        INTAKE[接入层<br/>Hook / API / Autopilot / 手动]

        subgraph P1[支柱1 · 上下文管理]
            RULES[Rules 资产<br/>hard/soft/safety 三级]
            SKILLS[Skills 资产<br/>复用现有 skill 实体]
            EXITDEF[节点准出字段定义]
        end

        subgraph P2[支柱2 · 工具系统]
            POOL[Agent 能力池<br/>能力画像 + 可用性]
            MATCH[调度匹配器<br/>指定/上一步/能力匹配/兜底]
            MCP[MCP 配置<br/>复用 agent.mcp_config]
        end

        subgraph P3[支柱3 · 执行编排]
            TMPL[WorkflowTemplate<br/>Node / Edge]
            ENGINE[工作流引擎<br/>DB 状态机 · Temporal 语义接口]
            FANOUT[Fan-out / AND 收敛]
        end

        subgraph P4[支柱4 · 状态与记忆]
            RUN[WorkflowRun / StepInstance]
            SUB[Submission 准出产物]
            ESTORE[(event_store<br/>持久化事件)]
        end

        subgraph P5[支柱5 · 评估与观测]
            VERDICT[Verdict<br/>pass/fail/blocked + L1-L4 证据]
            ACC[Acceptance<br/>通过 / 定向驳回]
            METRICS[指标聚合<br/>Agent/工作流/节点/场景 四层]
            DASH[观测大屏]
        end

        subgraph P6[支柱6 · 约束与恢复]
            GATE[门禁<br/>script / agent / rules]
            SWEEP[Sweeper 自愈<br/>workflow 级]
            CB[熔断转人工]
        end
    end

    subgraph RT[执行侧 · 现有]
        DAEMON[multica daemon<br/>14+ Agent CLI]
        TASKQ[(agent_task_queue)]
    end

    EXT -->|入站 Hook| INTAKE
    INTAKE -->|创建 Issue + Run 同事务| ENGINE
    ENGINE -->|EnqueueTaskForIssue 复用| TASKQ
    TASKQ --> DAEMON
    DAEMON -->|提交产物| SUB
    SUB --> GATE --> VERDICT
    VERDICT -->|pass 推进 / fail 重试返工 / blocked 暂停通知| ENGINE
    ENGINE --> FANOUT
    ENGINE --> SWEEP
    SWEEP --> CB
    ENGINE -->|End 节点| ACC
    ACC -->|approved 关闭 / rejected 定向返工| ENGINE
    RUN --> ESTORE --> METRICS --> DASH
    RULES --> GATE
    POOL --> MATCH --> ENGINE
    METRICS -->|出站 webhook| EXT
    ACC -->|通知| OPS
```

## 2. 六支柱子系统职责

### 支柱 1 · 上下文管理（知识资产层）

目标：让 Agent 在对的时间拿到对的信息，且信息经过质量过滤。

- **Rules 资产**（新增实体）：三级约束——`hard`（硬性红线，门禁机器可执行检查）、`soft`（软性约束，注入上下文推荐遵循）、`safety`（安全兜底，高风险操作前置确认）。scope 支持 workspace/project/agent 三层 [参考：团队规范文约束三级 + Rules 三层体系]
- **Skills 资产**：复用现有 `skill` / `skill_file` / `agent_skill` 实体 [已验证 `server/pkg/db/generated/models.go:752`]。新增能力：skill 与 workflow 节点绑定（节点 config 声明所需 skills）
- **准出字段定义**：节点模板声明 exit_fields schema（字段名/类型/必填/描述），上游 Agent 的产物按 schema 提交，下游节点直接消费结构化字段而非自然语言 [军团文 4.1]
- **知识质量过滤**：写入 Rules/Skills 前过 cairn 六级阶梯（流程机制，P2 落地为平台内的"知识待沉淀池"）[reference: cairn]

### 支柱 2 · 工具系统（Agent 能力池）

目标：平台知道有哪些 Agent、能做什么、现在能不能用、这一步该派谁 [军团文 3.1]。

- **Agent 实体复用**：现有 `agent`（status/runtime_mode/mcp_config/model/max_concurrent_tasks）[已验证 `models.go:24`]
- **能力画像**（新增 `agent_capability` 表）：capability_key + proficiency + 历史成功率证据，从 event_store 中任务结果持续回写 [推测：初期可由人工标注 + 简单统计，不做复杂画像模型]
- **调度匹配器**：四种策略对齐军团文——`指定 Agent`（节点 config 写死）/ `上一步指定`（上游 exit_fields 携带 assignee）/ `能力匹配`（required_capabilities × proficiency × 当前负载 × runtime 在线打分）/ `兜底解析`（LLM 路由或默认 Agent）。现有派发通道全部复用：`EnqueueTaskForIssue` [已验证 `server/internal/service/task.go:651`]
- **MCP**：复用 `agent.mcp_config` [已验证 `models.go:24`] + `runtime_mcp_overlay` [已验证 `models.go:124`]

### 支柱 3 · 执行编排（工作流引擎）

目标：系统知道下一步怎么走 [军团文 3.2]。

- **模板**：WorkflowTemplate + Node + Edge，声明式定义流程结构；发布时冻结快照，进行中的 Run 不受后续编辑影响
- **节点类型**：`agent`（Agent 执行）、`gate`（门禁）、`fan_out`（动态并行拆分）、`converge`（AND 收敛）、`acceptance`（人工验收）、`end`（结束+通知）
- **引擎**：DB 状态机（决策 D4）。消费 verdict 推进边；条件路由按 edge.condition 求值——表达式语言定为 **JSONLogic**（JSON 原生、无任意代码执行、Go/TS 均有实现，模板 publish 时做 schema 校验，非法表达式拒绝发布）[review 指摘，采纳；选型权衡见 tech-selection.md TS-8]
- **Fan-out**：fan_out 节点消费上游 submission 的子任务清单，动态创建 N 个并行子 StepInstance（各自独立 Issue + AgentTask）；converge 节点等全部子步骤终态后 AND 收敛；子任务失败策略（fail/block/rework）决定兄弟任务命运 [军团文 4.2]
- **接口按 Temporal 语义设计**（决策 D4）：Run≈Workflow、Step≈Activity、Verdict≈Signal、定时器≈Timer。第二阶段可平移 Temporal

### 支柱 4 · 状态与记忆

目标：上下文在系统里传递，不靠人转述 [军团文阶段小结]。

- **WorkflowRun / StepInstance**：运行状态机（见 §3 实体设计）
- **Submission**：每次 Agent 执行落成一次提交，含业务产物（artifacts）+ 准出字段（exit_fields）+ 摘要
- **step_transition（P0 即建）**：workflow 生命周期的轻量历史——每次 Step/Run 状态转移落一条（from/to/attempt/trigger_by/payload）。它是熔断计数、失败诊断七要素、rework_context 组装的直接数据源。**不等 P2 的 event_store**：没有它，P0/P1 的返工与诊断无历史可查，P2 再补历史会很痛 [review 指摘，采纳]
- **event_store（P2）**：全局平台事件持久化。现有事件总线是进程内 fire-and-forget [已验证 `server/internal/events/bus.go`]，新增 listener 将 86 个事件常量 + workflow 新事件追加写入 [已验证 `server/pkg/protocol/events.go`]，作为指标聚合与出站同步的统一数据源。分工：step_transition 是 workflow 域内的状态机历史（P0 必需）；event_store 是全平台事件的统一沉淀（P2 观测/出站用）
- **rework_context**：驳回原因 + 历史 verdict + 前序 exit_fields 打包（从 step_transition + verdict 组装），随新一轮任务注入 Agent 上下文；注入前做注入消毒（防存储型 prompt 注入）[军团文 4.3 + reference: cairn]

### 支柱 5 · 评估与观测

目标：Agent 完成 ≠ 业务完成；优化不靠感觉 [军团文 4.3/4.6]。

- **Verdict**：每次 Submission 由系统派生统一裁定：`pass / fail / blocked` + root_cause + confidence + evidence（按 L1 语法/L2 逻辑/L3 规范/L4 架构分层记录 [团队规范文评估四层]）。verdict_by 区分来源：agent 自评 / gate 脚本 / system / human。**生产与验收分离**：Executor Agent 不自评终态，Evaluator 角色（独立 Agent 或门禁）出 verdict [Harness 科普文 Anthropic 实践]。**写入权限契约**：verdict 写接口按 step 类型 + task 绑定鉴权——只有 gate/evaluator 类 step 的 task token（mat_）可写；executor 类 step 的 token 调用返回 403 [review 指摘，采纳]
- **Acceptance**：Run 到达 End 节点后进入验收；负责人 approved → 关闭；rejected → 指定节点 + 驳回原因 → 定向返工
- **指标四层** [军团文 4.6]：Agent 层（调用次数/成功率/blocked 率/超时率）、工作流层（完成率/一次通过率/平均耗时/返工率）、节点层（blocked 分布/失败原因/驳回原因）、场景层（通过率/人工介入率/采纳率）。从 event_store 聚合
- **观测大屏**：前端新路由段，四层指标 + Run 列表/详情 + 失败诊断视图

### 支柱 6 · 约束与恢复

目标：失败可见、可恢复、可交给人 [军团文 4.4]。

- **门禁（gate 节点）**五种形态：
  1. `script`：门禁脚本，经 daemon 以特殊 task 执行（隔离环境，对齐 CodeStable DoD runner 子进程模式），输出结构化 gate 结果（pass/block/warn + 明细）
  2. `agent`：门禁智能体（Evaluator 角色），带环境验证（跑测试/操作产物），不只读代码打分 [Harness 科普文：带环境的验证]
  3. `rules`：Rules 红线检查（hard 级规则机器可执行部分）
  4. `adversarial`：对抗式审查——新鲜上下文（仅 diff/产物/测试，不含需求与设计文档，消除 builder bias），产出不阻断、人工验收时暴露 [已验证 `harness/squad-briefing.md:158` Hybrid gate]
  5. `hybrid`：半硬组合——script 备料（跑出客观 facts：新增失败清单，不可推翻）+ agent 判定（对每条失败处置 fatal 阻断 / flaky 重试 / historical warn / acceptable）。`gate_run.output` 两层结构：`facts` + `dispositions` [已验证 `harness/squad-briefing.md:146-154` 半硬层 + gate-runner prompt；机制清单 D-4/D-5]
- **门禁安全契约**（P1 落地，设计在此冻结）[review 指摘，采纳]：
  1. 脚本来源 allowlist：仅允许模板内联脚本或仓库相对路径，禁止任意 URL/绝对路径
  2. 执行上限：超时（节点 config 可配，有全局上限）、stdout/stderr 输出大小上限（超限截断并标记）
  3. secrets 注入：默认不注入任何 workspace secret；节点显式声明的经 secretbox 解密注入 [已验证 `server/internal/util/secretbox` 存在]
  4. 网络限制：daemon 侧 egress 策略（默认仅平台 API + 声明的 allowlist 域名）[推测：依赖 daemon 执行环境能力，P1 验证]
  5. fix_hint 回灌前消毒：gate 输出回灌 Agent 上下文前做注入消毒（同 rework_context）
  6. Executor Agent 不可写 verdict（见支柱 5 写入权限契约）
- **Sweeper 所有权：两个 sweeper，边界清晰** [review 指摘，采纳]：

  | Sweeper | 文件 | 拥有 | 不拥有 |
  |---|---|---|---|
  | runtime sweeper（上游，不改） | `server/cmd/server/runtime_sweeper.go` [已验证] | runtime 掉线标记、卡死 task 失败、过期 queued task | workflow 语义 |
  | workflow sweeper（fork 新增） | `server/internal/workflow/sweeper.go`（新文件，main.go 注册数行） | workflow_run/step_instance 巡检：活跃 Step 无 AgentTask → 重激活；Step running 超 deadline → 重置/升级；blocked 超时 → 暂停 + 通知；rework 熔断计数 | 直接改 agent_task_queue 状态 |

  交互规则：workflow sweeper 发现 task 层异常时，只调 service 层既有函数（如 FailTask [已验证 `server/internal/service/task.go:2108`]），不直接 UPDATE agent_task_queue——避免与 runtime sweeper 双写同一状态 [军团文 4.4 三类场景由两者合力覆盖]
- **熔断转人工**：同一节点 rework 次数超阈值（默认 3，对齐 harness circuit_breaker [已验证 `harness/pipeline/standard.yaml:66-68`]；计数来源 step_transition）→ Run 暂停，Issue 转派人类负责人 + 通知
- **定向返工**：驳回/失败回到真正出问题的节点，rework_context 注入，不全链路重跑 [军团文 4.3]

## 3. 核心实体与 DB schema 草案

新增表（migration 900+ 号段，决策 D3）。只列表名 + 关键字段 + FK，不写完整 DDL（R6）。

### 901_workflow_core

| 表 | 关键字段 | FK / 关系 |
|---|---|---|
| `workflow_template` | id, workspace_id, key, name, description, version, status(draft/published/archived), created_by, timestamps | → workspace |
| `workflow_node` | id, template_id, node_key, type(agent/gate/fan_out/converge/acceptance/end), name, config JSONB（agent_selector/required_capabilities/instructions/exit_fields schema/timeout/gate 配置/fail 策略）, position JSONB（画布坐标） | → workflow_template |
| `workflow_edge` | id, template_id, from_node_id, to_node_id, condition JSONB（on verdict / exit_fields 表达式）, priority | → workflow_template, → workflow_node ×2 |
| `workflow_run` | id, workspace_id, template_id, template_snapshot JSONB（发布冻结）, status(running/completed/failed/cancelled/waiting_acceptance), source_type(issue/hook/autopilot/manual), source_id, context JSONB, started_at, completed_at | → workflow_template, → workspace |
| `step_instance` | id, run_id, node_key（快照内引用）, status(pending/active/dispatched/running/waiting_gate/passed/failed/blocked/rework/skipped), agent_id NULL, agent_task_id NULL, issue_id NULL, parent_step_id NULL（fan-out 子步骤）, attempt, exit_fields JSONB, started_at, finished_at, deadline_at | → workflow_run, → agent, → agent_task_queue [已验证 `models.go:92`], → issue [已验证 `models.go:512`], 自引用 parent_step_id |
| `submission` | id, step_instance_id, task_id, artifacts JSONB（pr_url/branch/文件/摘要等业务产物）, exit_fields JSONB, raw_summary text, idempotency_key, created_at | → step_instance, → agent_task_queue；UNIQUE(step_instance_id, attempt 维度) |
| `verdict` | id, submission_id, step_instance_id, result(pass/fail/blocked), root_cause text, confidence numeric, evidence JSONB（L1-L4 分层）, verdict_by(agent/gate_script/system/human), created_at | → submission, → step_instance；UNIQUE(submission_id) |
| `acceptance` | id, run_id, status(pending/approved/rejected), reviewer_id, reject_to_node_key NULL, reject_reason text, rework_context JSONB, decided_at | → workflow_run, → member |
| `step_transition` | id, run_id, step_instance_id, from_status, to_status, attempt, trigger_by(verdict/sweeper/human/system/engine), payload JSONB（verdict 摘要/驳回原因/重置原因）, created_at | → workflow_run, → step_instance。P0 即建：熔断计数、失败诊断、rework_context 的历史来源；与 P2 全局 event_store 分工见 §2 支柱 4 |

### 902_gates_rules

| 表 | 关键字段 | FK / 关系 |
|---|---|---|
| `rule` | id, workspace_id, name, level(hard/soft/safety), scope(workspace/project/agent), content text, config JSONB（globs/alwaysApply）, status, version, timestamps | → workspace |
| `rule_binding` | id, rule_id, target_type(node/template/agent/project), target_id, enforcement(gate_check/context_inject) | → rule |
| `gate_run` | id, step_instance_id, gate_type(script/agent/rules), script_ref NULL, status, output JSONB（pass/block/warn + 明细）, duration_ms, created_at | → step_instance |

### 903_events_integration

| 表 | 关键字段 | FK / 关系 |
|---|---|---|
| `event_store` | id, workspace_id, event_type, actor_type, actor_id, aggregate_type, aggregate_id, payload JSONB, created_at（追加写；按 created_at 分区或定期归档） | → workspace |
| `outbound_webhook` | id, workspace_id, name, target_url, secret, event_filters JSONB, status, retry_policy JSONB | → workspace |
| `outbound_delivery` | id, webhook_id, event_id, status(pending/delivered/failed), attempts, response_code, last_error, delivered_at | → outbound_webhook, → event_store。仿入站 `webhook_delivery` 审计 [已验证 `models.go:943`] |

### 904_agent_capability

| 表 | 关键字段 | FK / 关系 |
|---|---|---|
| `agent_capability` | id, agent_id, capability_key, proficiency smallint, evidence JSONB（成功率/样本量）, updated_at | → agent [已验证 `models.go:24`] |

### 与现有实体的关系原则

- **只加新表，不改上游表结构**。新表单向 FK 引用旧表（step_instance → agent_task_queue / issue / agent），反查走新表查询。若确需给上游表加列，用 900+ migration 加 nullable 列，上游无感知 [推测：目前设计中无此需求]
- **派发复用**：引擎创建 Issue（走 `IncrementIssueCounter` 同事务 [已验证 `server/internal/service/issue.go:206`]）+ 调 `EnqueueTaskForIssue` [已验证 `server/internal/service/task.go:651`]，不新造派发通道
- **状态联动**：verdict/acceptance 驱动 `issue.status` 变更（in_review/done/blocked），复用现有状态机与 realtime 广播

## 4. 关键数据流

### 4.1 主链路（标准需求）

1. **进入**：外部系统触发 Hook（携带标题/描述/负责人/来源链接/template_key [军团文 3.3]）→ 平台同事务创建 Issue + WorkflowRun（含模板快照）
2. **激活**：引擎激活首节点 → 创建 StepInstance → 按节点 agent_selector 调 `EnqueueTaskForIssue`
3. **执行**：daemon claim → Agent 执行（上下文含节点 instructions + 上游 exit_fields + 绑定 Rules/Skills + 本节点 exit_fields schema，Agent 执行前即知提交契约）→ 提交 Submission。**准出校验位置** [review 指摘，采纳]：exit_fields schema 随 template_snapshot 在 Run 启动时冻结；校验双层——submission API/CLI handler 层（入参即拒）+ service 层（兜底）；错误结构化返回（field 级 missing / type_mismatch），校验失败的 submission 不落库、不进入 verdict
4. **裁定**：gate 节点执行（如有）→ 派生 Verdict
5. **推进**：引擎消费 Verdict——`pass` 按 edge.condition 推进下一节点；`fail` 按节点策略 retry（attempt 递增）/rework/转人工；`blocked` 暂停 + 通知
6. **结束**：End 节点 → 完成通知（inbox [已验证 `models.go:494`] / Lark / Slack / 出站 webhook）→ Acceptance pending
7. **验收**：approved → Run completed + Issue done；rejected → 目标节点新建 StepInstance + rework_context 注入
8. **沉淀**：全程事件写 event_store → 指标聚合 → 大屏/出站同步

### 4.2 Fan-out 链路

上游节点 Submission 的 exit_fields 含子任务清单 → fan_out 节点动态创建 N 个子 StepInstance（各自独立 Issue + AgentTask，可分配给不同 Agent）→ 全部终态后 converge 节点 AND 收敛 → 子任务失败按策略（fail 全停 / blocked 暂停 / rework 单体重做）处理 [军团文 4.2]

### 4.3 自愈链路

Sweeper 周期巡检：①活跃 Step 无 AgentTask → 重新激活；②Step running 超 deadline → 重置为 active 重派发或升级；③Agent blocked 超时 → Run 暂停 + inbox 通知；④同一节点 rework ≥ 阈值 → 熔断转人工 [军团文 4.4 + harness circuit_breaker]

## 5. harness 约定 → 平台原语迁移表

harness 的真实契约分布在多个层面 [已验证：`harness/` 目录]：`pipeline/*.yaml`（2 份声明）、`squad-briefing.md`（运行契约，195 行）、`leader/leader-prompt.md`、`gates/`（13 个脚本）、`guides/`（11 份方法论）、`skills/`（4 角色 prompt + 子 skill + registry.json + sync 机制）、`cli/`（4 个工具）。**只映射 YAML 表面会在 P0/P1 实现时反复发现遗漏机制** [review 指摘，采纳]。以下按层完整映射（每条均已核对来源）：

### 5.1 pipeline YAML 层（standard.yaml + bugfix.yaml）

| harness 约定 | 平台原语 |
|---|---|
| `pipeline.stages`（standard 6 阶段 / bugfix 4 阶段） | `workflow_template` + `workflow_node`；P0 种子模板含 standard 与 bugfix 两份 |
| `gate: type script, hard, on_fail rework stage N` | gate 节点（type=script）+ `gate_run` + edge condition |
| `gate: type soft`（不阻断，findings 下一 human gate 暴露） | gate 节点 hard=false；findings 落 `gate_run.output`，验收视图聚合暴露（对应 scar 机制，见 5.2） |
| `human_gates`（Spec Freeze / Final Acceptance） | 节点 type=acceptance + `acceptance` 实体 |
| `auto_pass` / `auto_pass_condition`（bugfix 阶段 4：review APPROVED 且 baseline 无阻断则自动验收） [已验证 `bugfix.yaml`] | acceptance 节点 config：auto_pass 条件（JSONLogic 表达式，同 edge.condition 求值机制）；条件满足时自动 approved，否则等人工 |
| `circuit_breaker: threshold 3 → assign human` | 引擎熔断（§2 支柱 6，计数来源 step_transition） |
| `role: planner/implementer/reviewer/gate-runner/human` | 节点 config.agent_selector；human 角色 = acceptance 节点 reviewer |
| `produces: [prd.md, ...]` | 节点 exit_fields schema |
| `initial_status` | StepInstance 初始状态机 |

### 5.2 squad-briefing.md 运行契约层（最大缺口，逐条映射）

| harness 机制 [已验证 `squad-briefing.md`] | 平台原语 |
|---|---|
| **两层状态模型**：parent issue metadata KV（原子并发安全，仅队长写）+ child issue 评论 verdict block（各 agent 写，队长读） | 平台 DB 直接取代两层：`workflow_run.context`（运行级 KV，仅引擎写）+ `submission`/`verdict` 实体（执行者写，引擎读）。不再需要"评论里藏结构化块"的变通 |
| **命名铁律**：`verdict` 只表示流程裁定（pass/fail/blocked）；业务审查意见用 `decision`（APPROVED/CONDITIONAL/REJECTED），不得混用 | 实体设计遵守：`verdict.result`（pass/fail/blocked，引擎唯一消费的推进字段）与 `acceptance.status` + review decision（业务意见）分离。写入蓝图术语表，API 字段命名不得混用 |
| **Spec Freeze = assignee-swap**：不是 stage——队长把 parent assignee 改给人类 member → 平台停止自动唤醒 → 人评审 → 设 `frozen_spec` metadata → 改回队长继续 | acceptance 节点的平台原生表达：进入 acceptance 即暂停自动推进（无需 assignee 变通）；冻结标志位落 `acceptance`/`workflow_run.context` 字段；人操作后引擎继续 |
| **第 5 角色 human approver**（squad member type=member role=approver：Spec Freeze 评审、终验、熔断兜底） | `acceptance.reviewer_id`（→ member）；熔断转人工的接收角色；squad member role 扩展 [推测：role 字段是否已支持 approver 需实现时验证] |
| **test-plan.json**：项目级测试命令声明（`{unit:{cmd}, api:{cmd}}`，cmd:null=跳过），baseline/api gate 跨项目运行的前提；`detect_tests.py` 可扫 Makefile/package.json 生成草稿 | 新增项目级配置：`project` 资源或独立 `test_plan` 配置（JSONB），gate 节点 config 引用；P1 门禁落地的前置。无 test_plan 的 gate 按节点配置 SKIP 不阻断 |
| **baseline B−A 语义**：只 block 新增失败（after−before 差集），已知失败冻结在 before 快照 | gate 脚本语义规范：diff 型 gate 的阻断范围=新增失败；`gate_run.output` 结构含 baseline_ref 与 new_failures 字段 |
| **Hybrid/对抗式门禁**：阶段 5 后跑新鲜上下文对抗审查——不给 prd/design（消除 builder bias），只给 diff + test cases；产出不阻断，人工验收时暴露 | 第 4 种门禁形态 `adversarial`（或 agent 形态的 context_isolation 配置）：Evaluator 上下文白名单化（仅 diff/产物/测试，不含需求与设计文档）；output 聚合进验收视图 |
| **scar 持久化**（`scar_summary.py` 聚合非阻塞发现） | soft/adversarial gate 的 findings 落 `gate_run.output`；验收视图聚合展示（无独立表，查询聚合） |
| **rollback_counter.py 脚本计数，队长不自己数** | 平台引擎直接计数（step_transition），无需脚本变通；映射说明保留 |
| **gate-result.jsonl 报告载体**（每门禁 append，消费链：门禁→审查员→人） | `gate_run` 表（已设计） |

### 5.3 gates/ 脚本与 skills/cli 运营层

| harness 机制 | 平台原语 |
|---|---|
| 13 个 gate 脚本（plan_contract_check / baseline / api_gate / spec_freshness / verification_contract_check / workflow_integrity_check / rollback_counter / scar_summary / delivery_checklist / detect_tests / gate_prd_confirm / gate_result / task_resolver） | P1 script gate 的种子脚本库：平台提供内置脚本目录（模板内联或仓库路径，受安全契约 allowlist 约束）；脚本逻辑逐步平台原语化（如 rollback_counter → 引擎计数） |
| skills/ 4 角色 prompt + 子 skill（TDD/executing-plans/brainstorming/systematic-debugging 等）+ registry.json | 平台 Skills 资产的种子内容；角色 prompt → 节点 instructions 模板 |
| **skill sync + hash pinning + drift detection**（`cli/sync_skills.py` + `.source.json`） | Skills 资产运营机制（P2）：平台 skill 内容 hash 随任务下发，daemon 侧校验 drift；本地改动检测后上报 |
| `cli/resume.py`（中断恢复） | 引擎即恢复语义本身（Run/Step 持久化，Sweeper 重激活），无需独立工具 |
| guides/ 11 份方法论（evidence-and-quality-gates / executor-protocol / baseline-and-gate-result-protocol 等） | 平台 gate 检查项与证据契约的设计参照（P1 门禁规则库来源） |

### 5.4 迁移策略

P0 实现前先产出 **harness 机制清单**（从 squad-briefing.md + gates/ + guides/ 全量提取，标注 P0 必需 / P1 补充 / 不迁移），作为 schema 冻结的前置输入 [review 建议，采纳；已列入 roadmap P0 前置项]。harness YAML 作为 P0 模板种子（standard + bugfix 两份）；prompt 层 harness 在平台原语覆盖后逐步退役。

## 6. 上游兼容策略（fork 卫生，决策 D3）

| 措施 | 内容 | 依据 |
|---|---|---|
| migration 号段隔离 | 新表全部用 900+ 号段，字典序永远排在上游（当前最大 174）之后；**175-899 预留给上游**，fork 不占用。注意：lint 只强制"新 migration ≥149 且前缀唯一"（`migrations_lint_test.go:15,96-107`），并不显式保护 900+——**900+ 是蓝图自定的设计规则**，靠 fork 纪律与合并 playbook 维持，不靠 lint | [已验证 `server/internal/migrations/migrations.go:52-98` 按文件名字典序；`migrations_lint_test.go:15,96-107`] |
| 触点预算 | 上游文件修改 ≤5 个 × ≤3 行：`router.go` +1 行（registerWorkflowRoutes）、`listeners.go` +1 行（registerWorkflowListeners）、`main.go` 接线数行；其余全部新文件 | [已验证 `server/cmd/server/router.go:118` 单函数注册] |
| 新包边界 | `server/internal/workflow/`（引擎）、`server/internal/handler/` 新增 workflow_*.go、`pkg/db/queries/workflow_*.sql`、前端 `apps/web` 新路由段 + `packages/views/` 新目录 | multica 既有分层 [已验证 AGENTS.md 包边界规则] |
| feature flag 门控 | 所有新功能挂 `internal/featureflags`，上游合并时新代码默认休眠 | [已验证 `server/internal/featureflags` 存在] |
| sqlc 冲突解法 | 生成文件（models.go 等）冲突时接受任一侧后 `sqlc generate` 重新生成 | sqlc 产物可机械再生 |
| 合并 playbook | 每周或每上游 release 合并一次；顺序：migration 号段检查 → sqlc regenerate → router/listeners 单行冲突手动 → `make check` 全量验证 | [推测：频率可按上游活跃度调整] |
| 不改上游表 | 只加新表；必须加列时 900+ migration 加 nullable 列 | 见 §3 关系原则 |

## 7. 部署架构

- 沿用 multica 现有部署：Go server + PostgreSQL + Redis + daemon，docker-compose 自托管 [已验证 `docker-compose.selfhost.yml` 存在]
- 无新增基础设施（决策 D4：不引入 Temporal/NATS 等新依赖）；event_store 与业务库同 PostgreSQL
- 内部团队工具（决策 D5）：权限沿用现有 workspace/member 模型 [已验证 `models.go:968`（Workspace）、`models.go:671`（Member）]，不新增多租户设计

## 8. 并发与幂等设计（Q1 补充，P0 必须）

工作流引擎是并发系统：fan-out 并行子步骤可同时完成、Sweeper 与正常派发存在竞态、daemon/CLI 会重试。以下机制全部复用 multica 既有模式，P0 随引擎落地 [review 指摘，采纳]。

### 8.1 并发场景与对策

| 场景 | 风险 | 对策 |
|---|---|---|
| fan-out 多个子步骤同时完成 | converge 被并发触发，下游重复激活 | converge 的"计数子步骤终态 + 激活下游"在同一事务内完成，并对 run 行加锁（SELECT ... FOR UPDATE） |
| Sweeper 重激活 vs 正常派发 | 同一 step 被激活两次 | 状态转移守卫：条件 UPDATE（`SET status=$new WHERE id=$1 AND status=$expected`），影响 0 行即放弃并重读 [代码既有用法：multica task 生命周期同款模式] |
| 多 daemon 并发 claim | task 重复执行 | 复用现有 lease 机制（PrepareLeaseExpiresAt [已验证 `models.go:121`]；竞态测试 `task_claim_race_test.go`） |
| 验收驳回 vs 进行中 step | 驳回目标节点已有活跃 step | 驳回处理在事务内重读 run 当前状态，目标节点存在活跃 step 时拒绝或合并 |
| 重复 verdict 信号 | 下游节点重复推进 | 推进幂等：引擎处理 verdict 时在事务内重读 step 当前状态，已终态的 step 忽略重复信号 |

### 8.2 唯一约束（DB 层硬保证）

- `step_instance`：UNIQUE(run_id, node_key, parent_step_id, attempt)——同一 run 同一节点同一轮次只有一个 step
- `submission`：UNIQUE(step_instance_id, attempt)；idempotency_key 列支持 daemon/CLI 重试安全
- `verdict`：UNIQUE(submission_id)——一次提交只派生一个裁定
- `step_transition`：以 (step_instance_id, from_status, to_status, attempt) 去重，重复转移不重复落历史

### 8.3 对外契约的幂等语义

- 入站 Hook：外部系统重推同一工作（相同 source_type + source_id + template_key）时返回已存在的 Run，不重复创建（唯一约束 + 冲突返回 200 + run_id）
- 出站 webhook：at-least-once 投递，契约要求消费方幂等；outbound_delivery 记录 event_id 供消费方去重
- Acceptance 决策：UNIQUE(run_id) 部分索引（WHERE status='pending'），防止并发双击产生两个验收决定 [推测：部分索引语法 PG 支持，实现时验证]

## 9. 明确不做的（本蓝图范围内）

- 完整 IDE 编辑器（Monaco/LSP/远程终端）——决策 D1
- AI 原生工作流（路径 B）——只通过引擎接口与节点类型扩展性预留空间
- 插件 SDK——扩展仍走编译期，P3 再评估
- 对外产品化的多租户/计费/配额——决策 D5
