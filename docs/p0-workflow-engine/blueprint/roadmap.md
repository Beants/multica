# 蓝图 · 分阶段路线图

> 对齐军团文"第一阶段：先把人类流程 Agent 化"的务实策略（路径 C），四阶段推进。每阶段列交付物、依赖、可测出口标准。
> 阶段映射：P0/P1 ≈ 军团文第一阶段（骨架 + 能跑 → 可用）；P2 ≈ 军团文 7.1/7.2（标准化 + 可观测）；P3 ≈ 军团文 7.3（演进 + AI 原生预留）。

## 阶段总览

| 阶段 | 主题 | 对应军团文 | 关键交付 |
|---|---|---|---|
| P0 | 标准需求链路端到端 | 三根骨架最小集 + 4.1 准出 + 4.3 验收 | 工作流核心表 + 引擎（线性）+ Verdict + Acceptance + 入站 Hook |
| P1 | 六类能力补全 | 4.2 Fan-out + 4.4 自愈 + 4.5 诊断 + 门禁 | Fan-out/收敛、条件路由、门禁三形态、workflow 级 Sweeper、能力匹配路由、Rules 资产 |
| P2 | 观测与知识资产 | 4.6 指标 + 7.1 标准化 + 7.2 可观测 | event_store、出站 webhook、指标四层、观测大屏、知识沉淀池、场景扩展（Bug Fix/自迭代/历史问题池） |
| P3 | 演进 | 7.3 + 下一阶段 | Temporal 评估迁移、节点画布、Agent 标准协议正式化、AI 原生工作流实验 |

---

## P0 · 标准需求链路端到端

**目标**：一条边界清晰的标准需求，从外部进入到负责人验收关闭，全链路无需人逐步接力 [军团文 5.1]。

**交付物**：

| # | 交付 | 说明 |
|---|------|------|
| P0-1 | 901_workflow_core 全部 9 张表（含 step_transition） | migration 900+ 号段；sqlc 查询层；step_transition P0 即建（熔断计数/诊断/rework_context 的历史来源） |
| P0-2 | 工作流引擎（线性推进）+ 并发与幂等机制 | `server/internal/workflow/`；节点激活 → StepInstance → 复用 `EnqueueTaskForIssue` 派发；消费 verdict 推进边；retry（attempt）。并发与幂等按 design.md §8：状态转移守卫、唯一约束、推进幂等、Hook 幂等 |
| P0-3 | 节点类型：agent / acceptance / end | gate 节点 P1 到位；P0 的 verdict 由 Evaluator Agent（独立角色）或 system 派生；verdict 写接口按 step 类型鉴权（executor token 不可写） |
| P0-4 | Submission + Verdict 实体与 API | 准出字段 schema 随 template_snapshot 冻结；双层校验（handler + service），结构化错误返回；pass/fail/blocked + root_cause + confidence |
| P0-5 | Acceptance 机制 | End 节点 → 通知（inbox + Lark/Slack 复用现有集成）→ 负责人 approved/rejected；rejected 定向返工 + rework_context（注入前消毒） |
| P0-6 | 入站 Hook | 复用 autopilot webhook 模式扩展：外部系统 POST（标题/描述/负责人/来源链接/template_key）→ 同事务创建 Issue + Run；幂等（重复推送返回已有 Run） |
| P0-7 | 模板管理 UI（表单式） | 模板列表/详情/节点编辑（表单，非画布）；Run 列表/详情页（Step 状态、Submission、Verdict、step_transition 时间线） |
| P0-8 | 标准需求 + Bugfix 双模板种子 | standard（7 阶段，含独立 API 门禁，以 squad-briefing 为准）与 bugfix（4 阶段，默认人工验收；auto_pass 保留为 acceptance 节点能力、默认关，成熟场景再开启——D-12 裁决，2026-07-18 用户确认）两份模板 |
| P0-9 | feature flag + fork 卫生落地 | 全部新代码挂 flag；触点预算执行；合并 playbook 文档化 |

**P0 前置项（实现开工前）**：产出 harness 机制清单——从 `squad-briefing.md` + `gates/`（13 脚本）+ `guides/`（11 份）全量提取机制，标注 P0 必需 / P1 补充 / 不迁移，作为 schema 冻结的前置输入（对应 design.md §5.4）[review 建议，采纳]

**依赖**：无（第一个阶段）。

**出口标准**（可测）：

1. 一条模拟标准需求经 Hook 进入后，依次走完评审→分发→实现→验收 4 节点，全程无人手动转派，最终负责人收到完成通知
2. 负责人在 UI 驳回验收并指定"实现"节点，系统带驳回原因重建实现 StepInstance，Agent 新一轮任务上下文中可见 rework_context
3. Agent 提交的 Submission 缺少节点声明的必填准出字段时，系统拒绝并返回结构化错误（不进入 verdict）
4. 每个 Step 的 Issue/AgentTask/Submission/Verdict 在 Run 详情页可完整追溯
5. `make check` 全量通过；上游合并一次演练成功（冲突 ≤ 触点预算）

---

## P1 · 六类能力补全

**目标**：从"能跑"到"可用"——失败路径能处理 [军团文第四章]。

**交付物**：

| # | 交付 | 说明 |
|---|------|------|
| P1-1 | fan_out / converge 节点类型 | 动态子 StepInstance（独立 Issue+Task）；AND 收敛；子任务失败策略（fail/blocked/rework） |
| P1-2 | 条件路由 | edge.condition 用 JSONLogic 求值（TS-8）；publish 时 schema 校验；多出边 priority |
| P1-3 | gate 节点五形态 + 安全契约 | script（daemon 特殊 task 执行，结构化 pass/block/warn 输出）；agent（Evaluator 带环境验证）；rules（hard 级红线检查）；adversarial（新鲜上下文对抗审查，不阻断）；hybrid（script 备料 facts + agent 处置 dispositions 半硬组合）。安全契约按 design.md §2 支柱 6：来源 allowlist、超时/输出上限、secrets 规则、网络限制、fix_hint 消毒。前置：项目级 test_plan 配置（design.md §5.2） |
| P1-4 | 902_gates_rules 表 + Rules 资产 API/UI | 三级约束；rule_binding 绑定节点/模板/Agent；soft 级注入 Agent 上下文 |
| P1-5 | workflow 级 Sweeper | 活跃 Step 无 Task 重激活；running 超时重置；blocked 超时暂停+通知；rework 熔断转人工（阈值默认 3） |
| P1-6 | 失败诊断 | 失败记录七要素 [军团文 §4.5 实列 7 项]：①哪个 run/step/task；②调用了哪个 Agent/外部工具；③失败类型；④failure reason；⑤stderr/stdout 摘要；⑥是否重试；⑦最终状态（fail/blocked/等待人工）；Run 详情页诊断视图 |
| P1-7 | 904_agent_capability + 能力匹配路由 | 四种调度策略齐备（指定/上一步指定/能力匹配/兜底解析） |
| P1-8 | harness YAML 导入工具 | standard.yaml → WorkflowTemplate 自动生成 [推测：工作量小则提前到 P0] |
| P1-9 | 复杂流程模板 | 评审 → 技术方案 → 并行[配置变更/模板变更/代码变更] → 收敛 → 验收 [军团文 3.2 示例] |

**依赖**：P0 全部出口标准达成。

**出口标准**（可测）：

1. 复杂流程模板跑通：fan_out 拆出 ≥3 个并行子任务，全部完成后 converge 收敛推进；单个子任务失败按策略正确处理
2. 模拟 runtime 掉线 / task 卡死 / 活跃 Step 无 Task 三类故障，Sweeper 在配置周期内发现并恢复或升级，全程有事件记录
3. 同一节点连续 3 次返工后熔断：Run 暂停、Issue 转派人类、负责人收到通知
4. 任一失败 Step 的诊断视图能回答军团文 §4.5 的全部七要素
5. 能力匹配路由：声明 required_capabilities 的节点能自动选中符合能力且在线的 Agent；无匹配时走兜底解析
6. script gate 失败时按 edge 配置回到指定节点，gate_run.output 明细可查

---

## P2 · 观测与知识资产

**目标**：系统能被观察、被诊断、被持续优化；场景从标准需求扩展到四类 [军团文 4.6/第五章/7.2]。

**交付物**：

| # | 交付 | 说明 |
|---|------|------|
| P2-1 | 903_events_integration 表 + event_store 写入 | 新 listener 将现有 86 个事件常量 + workflow 事件追加落盘 [已验证 `pkg/protocol/events.go`] |
| P2-2 | 出站 webhook | outbound_webhook 配置 + outbound_delivery 投递（重试/签名/审计）；外部系统可订阅 Run/Step/Acceptance 状态 |
| P2-3 | 指标四层聚合 | Agent 层/工作流层/节点层/场景层，从 event_store 定时聚合 |
| P2-4 | 观测大屏 | 四层指标看板 + Run 监控墙 + 异常分布；前端新路由段 |
| P2-5 | 知识沉淀池 | cairn 六级阶梯流程平台化：对话/执行中标记候选 → 待沉淀池 → 批量提取 → 写入 Rules/Skills。成熟度 5 级追踪（draft/verified/proven/stale/**conflict**）[review 修正]；入口设强制触发信号约束（防沉淀池被低质量候选淹没）[review 建议，采纳] |
| P2-6 | 知识健康检查 | 双因子（时间 + 关联代码变更）标记 NEEDS_REVIEW [reference: cairn] |
| P2-7 | Bug Fix 场景模板 | 测试提交 Bug → Agent 修复 → 状态回写 → 测试验收 短闭环 [军团文 5.2] |
| P2-8 | 平台自迭代场景 | 平台自身改进项进入同一套机制（吃自己的狗粮）[军团文 5.3] |
| P2-9 | 历史问题池场景 | Autopilot 定期拉取积压问题 → 评审 → 处理 → 通知 [军团文 5.4]；复用现有 autopilot 触发器 [已验证 `models.go:210`] |
| P2-10 | 指标分析 Agent 雏形 | 定期回答"哪些 Agent 值得投入/哪些场景适合扩大/哪些节点可合并前置" [军团文 7.2] |

**依赖**：P1 全部出口标准达成；P2-7/8/9 依赖 P1 的门禁与 Fan-out。

**出口标准**（可测）：

1. 四层指标在大屏可查，数据来自 event_store 而非临时拉取；任一 Run 可从大屏下钻到 Step 诊断
2. 外部系统通过出站 webhook 收到 Run 状态变更（含重试与签名验证），投递记录可审计
3. Bug Fix 模板端到端跑通（模拟测试角色提交 → Agent 修复 → 验收关闭）
4. 平台自身一个真实改进项通过自迭代模板完成（分析→改动→验证→人工确认）
5. 历史问题池：配置 Autopilot 定期拉取后，符合条件的积压问题自动进入评审流程
6. 知识沉淀池：一次执行中标记的候选经批量提取后写入 Rules/Skills，成熟度状态可追踪

---

## P3 · 演进

**目标**：底座更稳、更标准；为 AI 原生工作流留出空间 [军团文第七章]。本阶段为评估与演进，不设强制交付。

**候选方向**：

| # | 方向 | 触发条件 |
|---|------|---------|
| P3-1 | Temporal 迁移评估 | P0-P2 运行数据显示：长任务恢复/重试/定时器自研成本 > Temporal 运维成本。引擎接口已按 Temporal 语义设计（决策 D4），迁移不改节点/模板 API |
| P3-2 | 节点画布（React Flow 可编辑） | 模板数量与复杂度超过表单可管理阈值 [军团文 7.1 工作流配置升级] |
| P3-3 | Agent 标准协议正式化 | 状态推进/结果提交/上下文查询从 prompt 约定升级为 CLI/MCP 统一协议 [军团文 7.1] |
| P3-4 | AI 原生工作流实验 | 用 P2 指标回答"哪些节点是角色分工遗留/哪些可由规则自动完成"，试点 目标理解→约束抽取→计划生成→工具执行→自动验证→风险判断→人类验收 新结构 [军团文 7.3] |
| P3-5 | 插件 SDK 评估 | 扩展需求超过编译期扩展可维护阈值 |

**依赖**：P2 完成；P3-4 依赖 P2 的指标数据积累（通常 ≥1 个季度真实运行）。

**出口标准**：Temporal 迁移/画布/协议三项各有明确 go/no-go 决策记录，依据为 P0-P2 沉淀的运行指标而非直觉。

---

## 风险与缓解

| 风险 | 影响 | 缓解 |
|---|---|---|
| 上游合并冲突累积 | P0 起持续 | 触点预算 + 号段隔离 + 每周合并 playbook（design.md §6）；冲突超预算时暂停新功能先还债 |
| 自研引擎范围蔓延（重造 Temporal） | P1-P2 | 节点类型白名单制，新增节点类型需评审；接口按 Temporal 语义约束，超语义需求记为 P3-1 迁移信号 |
| Agent 输出不稳定导致 verdict 失真 | P0 起 | 准出字段 schema 强校验 + 生产验收分离（Evaluator 独立）+ confidence 低时转人工 [军团文 4.1] |
| 场景扩张过快 | P2 | 四类场景按 标准需求→Bug Fix→自迭代→历史问题池 顺序逐个开通，前一个出口标准达成再开下一个 |
| 指标变成"好看的 Dashboard" | P2 | 指标设计绑定具体优化问题（军团文 4.6 表格），P2-10 指标分析 Agent 倒逼数据可用性 |
