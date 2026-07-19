# 蓝图 · 技术选型

> 每项选型：候选 ≥2 + 权衡 + 结论。已决策项（D3/D4）在此展开接口设计。

## TS-1 工作流引擎（决策 D4 展开）

| 候选 | 说明 |
|---|---|
| A. 自研 DB 状态机 | PostgreSQL 表 + 现有 scheduler/sweeper 基建；与 multica 风格一致（sqlc/migration/service 层） |
| B. Temporal | 成熟 durable execution；军团文下一阶段的选择 |
| C. 轻量任务库（River/Asynq 类） | 只解决任务队列与重试，不解工作流编排语义 |

**权衡**：B 功能最全但引入新基础设施（Temporal server 集群 + 独立 DB），与 multica "self-hosted 简单部署"定位冲突 [推测：docker-compose 自托管用户对新增组件敏感]；且 fork 与上游差异拉大。C 层级太低，工作流语义（模板/边/收敛/返工）仍要自研。A 第一阶段需求（线性 + fan-out + 返工）完全够用，风险是长任务恢复/定时器/可见性重造轮子——用接口语义约束缓解。

**结论**：A 先行（已决 D4）。引擎接口按 Temporal 语义设计，B 作为 P3-1 演进预案：

```
WorkflowEngine 接口（Go，server/internal/workflow/）：
  StartRun(ctx, templateID, source) -> Run        // ≈ Temporal StartWorkflow
  SignalVerdict(ctx, stepID, verdict) -> error    // ≈ Temporal SignalWorkflow
  EvaluateEdges(ctx, stepID) -> []StepInstance    // 引擎内部：verdict → 下一节点
  RequestRework(ctx, runID, nodeKey, ctx) -> error // 定向返工（驳回/失败）
  // Run ≈ Workflow；StepInstance ≈ Activity；Verdict ≈ Signal；deadline_at ≈ Timer
```

迁移信号（满足 ≥2 条即触发 P3-1 评估）：①节点平均耗时 > 30min 的流程占比显著；②定时器/重试类 bug 反复出现；③需要 Saga/动态分支等复杂语义。

## TS-2 事件持久化

| 候选 | 说明 |
|---|---|
| A. PostgreSQL event_store 表 | 与业务库同库，追加写，事务友好，sqlc 查询 |
| B. NATS / Kafka | 高吞吐事件流，天然支持多消费者 |
| C. Redis Stream | 复用现有 Redis，轻量流 |

**权衡**：B/C 引入或复用消息基础设施，但第一阶段事件量小 [推测：内部团队规模，日事件量万级以下]，且事件写入需要与业务事务一致（如 Run 创建与事件落盘同 tx）——只有 A 能同事务。B 的多消费者优势在 P2 出站 webhook 时有价值，但 outbound_delivery 表 + 重试轮询同样可达。

**结论**：A。event_store 按 created_at 分区或定期归档控制体积；出站投递用 outbound_delivery 轮询表实现（at-least-once）。B 作为远期高吞吐选项记录。

## TS-3 前端编排画布

| 候选 | 说明 |
|---|---|
| A. 表单优先 + 只读 DAG 视图 | P0/P1 模板编辑用表单；React Flow 只读渲染 Run/模板结构 |
| B. React Flow 可编辑画布 | 一步到位，拖拽编排 [军团文 7.1 节点画布方向] |
| C. 自研画布 | 完全可控，成本最高 |

**权衡**：multica 前端无画布依赖 [已验证：package.json 无 xyflow/react-flow]。B 是终态体验但 P0 模板少（2-3 个）、结构简单，表单足够；可编辑画布的交互细节（连线校验/撤销/对齐）会拖慢 P0。C 无必要。

**结论**：A。P0 表单 + `@xyflow/react` 只读视图；P3-2 升级为可编辑画布，触发条件为模板复杂度超表单可管理阈值。`@xyflow/react` 的"依赖小 / React 19 兼容 / 事实标准"为 [推测]，P0 引入依赖时以实际 build + typecheck 验证为准，不兼容则降级为纯表单向渲染 [review 指摘，采纳]。

## TS-4 Agent 标准协议

| 候选 | 说明 |
|---|---|
| A. multica CLI 扩展 | 新增 `multica submission create` / `multica verdict get` / `multica step context` 等命令；复用 mat_ task token [已验证 `models.go:840`] |
| B. MCP server | 平台暴露 MCP server，Agent 以工具调用方式交互 |
| C. 纯 prompt 约定 | 现状 harness 模式：CLI 读 issue/写评论，语义靠 prompt |

**权衡**：C 是现状，但"自然语言对暗号"不稳定 [军团文 7.1 明确指出要标准化]。A 与 B 不互斥：CLI 是 daemon 环境下 Agent 的主交互通道 [已验证：daemon 注入 CLI + mat_ token]；MCP 适合支持 MCP 的 Agent 直接挂载。

**结论**：A 先行（P0 随引擎落地 submission/verdict 命令），B 同期提供 stdio MCP server 薄包装（复用同一 service 层，daemon 注入 mcp_config），C 作为过渡兼容。P3-3 正式化为版本化协议。

## TS-5 门禁执行机制

| 候选 | 说明 |
|---|---|
| A. daemon 执行（特殊 task） | gate 脚本作为特殊 AgentTask 由 daemon 在目标环境执行，输出结构化 gate 结果 |
| B. 平台内嵌解释器 | server 内执行脚本（如 Starlark/JS sandbox） |
| C. 独立 gate 服务 | 单独部署的门禁执行服务 |

**权衡**：B 在 server 进程内执行用户脚本，安全风险高且环境（依赖/工具链）与代码实际环境不一致——门禁的价值恰恰是"在真实环境里验证"（跑测试/lint/构建）[Harness 科普文：带环境的验证]。C 部署成本高，内部工具不必要。A 复用现有 task 通道与隔离模型，对齐 CodeStable DoD runner 子进程模式。

**结论**：A。gate 节点产生 `gate-runner` 类型 task；脚本来源为模板内联或仓库路径；输出契约：`{result: pass|block|warn, checks: [{name, status, detail, fix_hint}]}`——fix_hint 直接回灌 Agent 上下文推动修复 [Harness 科普文 OpenAI 实践：检查结果带修复提示]。

## TS-6 轻量编辑器 / diff review（决策 D1 边界内）

| 候选 | 说明 |
|---|---|
| A. react-diff-view 类组件 + 简单编辑 | diff 渲染用成熟轻量组件；简单修改用代码高亮编辑器（如 CodeMirror） |
| B. Monaco | 完整 IDE 编辑器体验 |
| C. 仅链接 GitHub PR | 复用现有 GitHub 集成 [已验证 `server/internal/handler/github.go`]，平台内不做 review |

**权衡**：D1 明确"对齐 Orca，简单 review + 简单修改"。B 违反 D1（体积大、LSP 复杂）。C 最省但把验收关键动作（看改动）推出平台，验收链路断裂。

**结论**：A + C 组合。平台内嵌 diff 视图（数据源：GitHub PR diff 或 daemon 上报的 patch），支持行内评论（落成 issue comment）；简单修改用 CodeMirror 级编辑器（单文件编辑，无 LSP）；复杂修改跳转 GitHub。

## TS-7 能力匹配路由

| 候选 | 说明 |
|---|---|
| A. 规则打分 | required_capabilities × proficiency × 当前负载 × runtime 在线，加权打分取最优 |
| B. LLM 路由 | 让 LLM 根据 Agent 描述与任务内容选择 |
| C. 纯人工指定 | 现状 |

**权衡**：B 灵活但不可解释、不可复现，且路由是高频低延迟路径 [推测：每次节点激活都调用]。C 无扩展性。A 可解释、可调权，证据来自 agent_capability 持续回写。

**结论**：A 为主，B 作为"兜底解析"策略（无匹配或置信度低时 LLM 介入，对齐军团文四种调度策略）。路由决策记录到 step_instance（哪个策略、选中谁、打分明细），供 P2 指标层分析路由质量。

## TS-8 条件表达式语言（edge.condition）[review 指摘，采纳]

| 候选 | 说明 |
|---|---|
| A. JSONLogic | JSON 原生规则格式（`{"==":[{"var":"verdict.result"},"pass"]}`）；无任意代码执行；Go/TS 均有成熟实现；前后端可共享求值与校验 |
| B. CEL（Common Expression Language） | 表达能力更强（字符串/集合/函数），Google 系；但引入新运行时依赖，表达式可读性对非工程师不友好 |
| C. 受限 comparator（自研字段-操作符-值三元组） | 最简单、完全可控；但 AND/OR/NOT 嵌套与类型系统要自己设计，迟早重造 JSONLogic |

**权衡**：edge.condition 是模板作者写的、引擎在关键路径上求值的东西，必须满足：①不可执行任意代码（安全）；②publish 时可静态校验（错误左移）；③前后端都能求值（前端预览路由、后端实际推进）。三者 A 都满足；B 能力过剩且依赖重；C 的"简单"是幻觉，嵌套逻辑需求（如 `verdict=pass AND exit_fields.risk=low`）一出现就会失控。

**结论**：A。模板 publish 时对全量 edge.condition 做 JSONLogic schema 校验，非法表达式拒绝发布；求值上下文限定为 `{verdict, exit_fields, run.context}` 三个命名空间，不暴露其他运行时数据。

## 选型汇总

| # | 选型 | 结论 | 阶段 |
|---|------|------|------|
| TS-1 | 工作流引擎 | 自研 DB 状态机（Temporal 语义接口），Temporal 为 P3 预案 | P0 |
| TS-2 | 事件持久化 | PostgreSQL event_store + outbound_delivery 轮询 | P2 |
| TS-3 | 编排画布 | 表单 + React Flow 只读；P3 升级可编辑 | P0/P3 |
| TS-4 | Agent 协议 | CLI 扩展先行 + MCP 薄包装；P3 正式化 | P0/P3 |
| TS-5 | 门禁执行 | daemon 特殊 task，结构化输出带 fix_hint；安全契约见 design.md §2 支柱 6 | P1 |
| TS-6 | 编辑器 | diff 组件 + CodeMirror 简单编辑 + GitHub 跳转 | P1 |
| TS-7 | 能力匹配 | 规则打分为主 + LLM 兜底 | P1 |
| TS-8 | 条件表达式语言 | JSONLogic，publish 时校验，求值上下文三命名空间 | P1 |
