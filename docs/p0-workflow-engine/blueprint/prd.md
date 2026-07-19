# 多Agent协作IDE整体蓝图

## Goal

基于 multica 二次开发，参考 reference/ 下五个项目的优秀实践与 docs/ 三份方法论文档，设计一个覆盖软件工程生命周期的多 Agent 协作 Web 平台（"面向未来的 IDE"）的**整体蓝图**：架构设计 + 路线图 + 技术选型 + 实践采纳清单。本期只交付蓝图，不写实现代码。

核心参考：`docs/multica-ai-army-loop-engineering.md` —— 把 multica 从"Agent 任务管理"扩展成"多 Agent 协作工作流底座"，即三根骨架（Agent 可调度能力池、工作可编排、外部可交接）+ 六类能力（Verdict 准出、Fan-out 并行收敛、验收返工、自愈显式阻塞、错误诊断、指标看板）。

## Product Decisions（用户已确认，2026-07-17）

- D1 产品边界：编排控制台为主，不做完整 IDE。包括：①流程编排与控制（流程编排、智能体安排、门禁脚本/门禁智能体安排、验收规则、软件工程全生命周期控制）；②观测与管理（整体观测、任务管理大屏）；③编辑器对齐 Orca，支持简单 review 和简单修改（不做 Monaco/LSP/远程终端全家桶）
- D2 蓝图组织框架：平台 = 组织级 Harness，用 Harness 六支柱作为蓝图子系统划分框架。映射：上下文管理→准出字段/知识资产；工具系统→Agent 能力池/MCP/Skills；执行编排→工作流引擎/Fan-out；状态与记忆→Step Instance/事件存储；评估与观测→Verdict/Acceptance/L1-L4 评估/指标大屏；约束与恢复→门禁（Rules+脚本+Agent）/Sweeper/返工
- D3 改造策略：A. in-repo 扩展。引擎放 `server/internal/workflow/` 新包；migration 用 900+ 号段隔离；路由/监听各改 1 行；feature flag 门控；定期合并上游 + 冲突 playbook。关键依据：事件总线进程内（`server/internal/events/bus.go`）、Issue ID 在 service 层事务内分配（`server/internal/service/issue.go:206`）、migration 按文件名字典序（`server/internal/migrations/migrations.go:52-98`）
- D4 工作流引擎：自研轻量 DB 状态机先行（PostgreSQL + 现有 scheduler/sweeper 基建），引擎接口按 Temporal 的 workflow/activity 语义设计；Temporal 作为第二阶段演进预案写入蓝图
- D5 平台定位：内部团队工具优先。权限/多租户沿用 multica 现有模型不深化，部署文档从简
- D6 蓝图形态：放任务目录 `.trellis/tasks/07-17-agent-ide-blueprint/`，多文档（design.md / roadmap.md / tech-selection.md / practice-adoption.md）

## Confirmed Facts（代码调研已验证，含 file:line 锚点）

### multica 现状（Go + Next.js monorepo）

- 后端：Go 1.26 + Chi v5 + PostgreSQL（sqlc 代码生成，174 个 migration）+ Redis（realtime relay）。`server/cmd/server/router.go:118`
- 前端：`apps/web` Next.js App Router + React 19 + Zustand + React Query；`apps/desktop` Electron；共享包 `packages/core|ui|views`；无画布库依赖（画布选型是开放决策）
- 实时：gorilla/websocket，scope 房间模型（workspace/user/task/chat/daemon_runtime）。`server/internal/realtime/hub.go`
- 核心实体：Agent（status/runtime_mode/mcp_config/model，`models.go:24`）、AgentRuntime、Issue（status/priority/assignee 多态/parent_issue_id/acceptance_criteria JSONB/stage）、AgentTaskQueue（`models.go:92`；状态含 queued/dispatched/running/waiting_local_directory/deferred + completed/failed/cancelled 终态 [已验证 `server/pkg/db/queries/agent.sql:330`]；attempt/max_attempts/escalation/handoff_note；runtime_mcp_overlay 字段在 `models.go:124`）、Squad（leader 路由）、Skill（+SkillFile+agent_skill 绑定）、Autopilot（`models.go:159`）+ AutopilotTrigger（`models.go:210`，schedule/webhook/api/event）、Comment、InboxItem、ActivityLog、TaskMessage（执行轨迹）
- 派发策略现状：直接指派、squad leader 路由、评论 @ 触发、autopilot、chat。**无能力匹配路由**。`server/internal/service/task.go:651`
- 已有自愈雏形：runtime sweeper（掉线标记/卡死任务失败/过期 queued）。`server/cmd/server/runtime_sweeper.go`；autopilot failure monitor
- 事件总线：进程内同步 pub/sub，86 个事件常量 [已验证 `server/pkg/protocol/events.go`]，**无持久化事件存储**。`server/internal/events/bus.go`
- Webhook：仅入站（autopilot trigger、GitHub、Stripe）。**无出站 webhook/回调**
- 指标：Prometheus 业务指标 + PostHog + task_usage 表。`server/internal/metrics/business.go`
- Agent 接入：14 个 provider backend（claude/codex/qoder/kiro 等），daemon 在本机执行，task-scoped token（mat_）。`server/pkg/agent/agent.go:187-214`
- 已有集成：Lark/飞书、Slack 双向；GitHub App；Composio（MCP overlay）
- `harness/pipeline/standard.yaml` 已是工作流的概念原型：6 阶段（Plan → Plan Gate → Implement → Baseline Gate → Review → Acceptance）、角色槽、script gate（hard/soft）、on_fail rework、human gates（Spec Freeze / Final Acceptance）、circuit breaker（阈值 3 → 转人工）。但它只是 Leader agent 读的 prompt 约定，非平台原语。`harness/` 还有 gates/ leader/ skills/ guides/ cli/
- `.trellis/spec/backend|frontend/` 目前是未填充模板；guides/ 有 code-reuse 与 cross-layer 两份思考指南（实现阶段用）

### multica 缺口（与文档三根骨架 + 六类能力一一对应）

| 缺口 | 对应文档能力 |
|---|---|
| 无 workflow template 实体（DB 级） | 骨架 2 工作可编排 |
| 无工作流执行引擎（状态机/转移/重试/分支） | 骨架 2 |
| 无平台级验收/门禁原语（Acceptance 仅靠 harness prompt 约定） | 验收返工 |
| 无 DAG 并行 fan-out/fan-in（仅 squad leader 约定式拆分） | Fan-out |
| 无出站 webhook/回调 | 骨架 3 外部可交接 |
| 无条件路由 | 工作可编排 |
| 无定向返工流 | 验收返工 |
| 无能力匹配路由 | 骨架 1 Agent 可调度 |
| 无持久化事件存储（事件 fire-and-forget） | 诊断/指标 |
| 无插件 SDK（扩展均为编译期） | 长期扩展性 |

### reference/ 可借鉴实践（调研结论，按优先级）

- P0：Trellis 四阶段生命周期（Plan/Implement/Verify/Finish + 子智能体派发）；Orca worktree 隔离 + 派发 preamble + 心跳监控；CodeStable 机器可执行 DoD 门禁（checklist YAML → 子进程跑命令 → 结构化 gate 结果）
- P1：cairn 六级知识质量阶梯 + 单一知识源（.cairn/ + 薄 agent 适配层）；Orca/Trellis 类型化 Agent 间消息协议；Trellis 证据分级（[已验证]/[推测]）
- P2：LiveAgent skill 访问策略（会话级白名单 + 内置保护）；cairn 双因子健康检查（时间 + 代码变更）；LiveAgent MCP per-server 调用锁；CodeStable 风险分级路由（Quick/Standard/Goal）；cairn 注入消毒
- P3：CodeStable 结果导向 skill 评测（seed repo + 隐藏测试，成本高暂缓）

### docs/ 方法论输入（三份文档）

- **AI 军团文**（`docs/multica-ai-army-loop-engineering.md`）：三根骨架 + 六类能力 + 四类场景（标准需求/Bug Fix/平台自迭代/历史问题池）+ 下一阶段方向（Temporal、节点画布、Agent 标准协议、事件标准化、指标分析 Agent、AI 原生工作流）
- **Harness Engineering 科普文**（`docs/harness-engineering-explained.md`）：Prompt→Context→Harness 三次迁移；Harness 六层 = 上下文管理/工具系统/执行编排/状态与记忆/评估与观测/约束校验与失败恢复；Anthropic 实践（Context Reset、planner+generator+evaluator 生产验收分离、带环境验证）；OpenAI 实践（人只设计环境不写代码、渐进式披露 AGENTS.md ~100 行、CI 校验文档新鲜度 + 文档园丁 Agent、Agent 自验证闭环 6h+、架构约束写成机器可执行规则并带修复提示、后台 Agent 扫描腐化 + 质量分 + 自动重构 PR）
- **团队落地规范文**（`docs/harness-engineering-team-ai-coding-spec.md`）：六支柱映射工具链；3+1 Phase（Planner→Generator→Evaluator→Archiver）；评估四层 L1 语法/L2 逻辑/L3 规范/L4 架构；约束三级（硬性红线 Rules/软性约束 Skills/安全策略 Safety）；team-harness 仓库（Rules/Skills 单一来源 + 同步脚本）；Rules 三层体系（User/Team/Project）；harness-audit Skill（规范的可执行版本，7 维度打分/诊断/开方）；协作红线（先 Spec 后 Code 等）；反模式清单

## Requirements

- R1: 蓝图须覆盖目标架构：按 D2 六支柱框架划分子系统，给出核心实体与 DB schema 草案、服务边界、关键数据流（工作进入→编排→执行→验收→返工→指标）、与 multica 现有实体（Issue/AgentTaskQueue/Squad/Autopilot/Skill）的关系映射、harness 约定→平台原语迁移表
- R2: 蓝图须给出分阶段路线图（对齐军团文"第一阶段：先把人类流程 Agent 化"的务实策略），每阶段有明确交付物、依赖、可测出口标准；覆盖软件工程全生命周期视角：需求→评审→分发→实现→验证→验收→发布/关闭，以及平台自我迭代与历史问题池场景
- R3: 蓝图须完成关键技术选型并给出权衡：工作流引擎（按 D4 展开接口设计）、事件持久化、前端编排画布、Agent 标准协议、门禁执行机制、轻量编辑器/diff review 方案。每项 ≥2 候选 + 权衡 + 结论
- R4: 蓝图须明确 reference/ 五项目 + docs/ 三文档的每条实践：采纳/不采纳/暂缓 + 理由 + 目标阶段
- R5: 蓝图须按 D3 给出与 multica 上游的兼容策略：900+ migration 号段、触点预算（≤5 文件 × ≤3 行）、feature flag 门控、合并 playbook、sqlc 生成文件冲突解法
- R6: 蓝图论断须带证据等级标注（[已验证]/[推测]/[待业务确认]），实体设计给到表名 + 关键字段 + FK 关系（不写完整 DDL）

## Acceptance Criteria

- [x] AC1: `design.md` 落盘：六支柱子系统架构图（mermaid）、核心实体与 DB schema 草案、与 multica 现有实体的关系映射、harness→平台原语迁移表、上游兼容策略（对应 R1/R5/R6）
- [x] AC2: `roadmap.md` 落盘：≥3 个阶段，每阶段列交付物、依赖、可测出口标准；第一阶段对齐军团文"标准需求链路端到端"（对应 R2）
- [x] AC3: `tech-selection.md` 落盘：每个选型含 ≥2 候选、权衡、推荐结论；至少覆盖工作流引擎、事件持久化、前端编排画布、Agent 标准协议、门禁执行机制、编辑器方案（对应 R3）
- [x] AC4: `practice-adoption.md` 落盘：每条实践标注采纳/不采纳/暂缓及理由、目标阶段（对应 R4）
- [x] AC5: 蓝图通过用户 review 并确认可进入实现规划（2026-07-17 用户确认；经两轮外部 review 修订：并发幂等 §8、sweeper 所有权、step_transition、JSONLogic、gate 安全契约、harness §5 全层映射、事实错误修正）

## Out of Scope

- 任何实现代码（本期只出蓝图）
- 桌面/移动端形态（D1 已确认 Web 平台优先）
- AI 原生工作流（军团文路径 B，属长期方向，蓝图只留接口空间）
- 多租户/权限模型深化、对外产品化设计（D5 内部优先）

## Open Questions

无阻塞问题。剩余设计决策均属蓝图自身内容，按 D1-D6 在蓝图中给出。
