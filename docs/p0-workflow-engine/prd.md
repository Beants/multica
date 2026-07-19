# P0：标准需求链路端到端

## Problem

multica 已有 Agent/Issue/Task/Squad 等单任务管理能力，但一条真实工作链路（评审→分发→实现→验收）仍要靠人逐步转派：系统里没有可运行的流程结构（无 workflow template 实体、无执行引擎、无平台级验收原语、无结构化准出与裁定）。本任务落地蓝图 P0，把"人知道下一步怎么走"变成"系统知道下一步怎么走"。

## Goal（Success Metrics）

实现蓝图 P0：在 multica 仓内（in-repo fork，决策 D3）落地工作流引擎（线性推进）+ 901_workflow_core 10 张表 + 标准需求链路端到端——一条标准需求从外部 Hook 进入，经评审→分发→实现→验收，全程无人接力，负责人验收关闭或定向驳回返工。成功度量 = AC1-AC9 全部达成。

本任务为**父任务**：持有 P0 出口标准与波次（Wave 1-3）集成 review；实现按 Wave 分派子任务/子智能体执行。

## Users

- 平台内 Agent（executor/evaluator 角色）：经 CLI/API 提交产物与裁定
- 负责人/验收人（内部团队成员）：接收入站通知，在 UI 验收或驳回
- 外部工作系统（需求/缺陷系统）：经 Hook 推入工作、查询进度
- 后续维护者：P1/P2 阶段在本任务的引擎与实体上扩展门禁、Fan-out、观测

## Constraints

- fork 卫生：900+ migration 号段；触点 ≤3 个上游文件 × ≤3 行；feature flag `workflow_engine` 默认关
- 不改上游表结构；复用既有派发/状态机/realtime 通道（EnqueueTaskForIssueWithHandoff、service 层状态函数）
- 范围裁剪：P0 无线性外能力（无 gate 节点/fan-out/条件路由求值/Rules/能力匹配/sweeper/event_store/出站 webhook）
- 仓内规则：CLAUDE.md（API Compatibility zod parseWithFallback + malformed-response 测试、Web/Desktop 双端 wiring、Backend UUID Rules）

## 设计输入（已冻结）

- 蓝图：`.trellis/tasks/archive/2026-07/07-17-agent-ide-blueprint/`（design.md §2 六支柱/§3 实体/§6 fork 卫生/§8 并发幂等；tech-selection.md TS-1/TS-4/TS-8）
- 机制清单：`.trellis/tasks/archive/2026-07/07-17-harness-mechanism-inventory/inventory.md`（74 条机制 + D-1~D-12 差异）
- 关键已决：D3 in-repo 扩展（900+ migration 号段、触点 ≤5 文件 × ≤3 行、feature flag 门控）；D4 自研 DB 状态机（Temporal 语义接口）；D5 内部团队工具

## Requirements（对应 roadmap P0-1 ~ P0-9）

- R1（P0-1）：901_workflow_core **10 张表**（workflow_template/workflow_node/workflow_edge/workflow_hook/workflow_run/step_instance/submission/verdict/acceptance/step_transition）+ sqlc 查询层；migration 用 900+ 号段；并发约束按蓝图 §8.2（UNIQUE 硬保证；step_instance 用 UNIQUE NULLS NOT DISTINCT）
- R2（P0-2）：工作流引擎 `server/internal/workflow/`：StartRun / SignalVerdict / EvaluateEdges / RequestRework（Temporal 语义，TS-1）；线性推进 + retry（attempt）；激活策略只预创建下一节点（清单 D-3）；Run 初始化语义（清单 2.8）；并发与幂等按蓝图 §8（状态转移守卫、推进幂等、Hook 幂等）；**最小熔断：同节点连续 rework ≥3 → Run 暂停 + 转人工 + 通知（清单 1.19/2.6/3.6 为 P0 必需，从 roadmap P1-5 提前）**
- R3（P0-3）：节点类型 agent / acceptance / end；节点 config 增加 role 字段（executor/evaluator/reviewer）；**verdict actor 模型：executor step 由 system 从 submission 派生 verdict，evaluator step 经 `multica verdict create` 写 verdict，executor token 调 verdict 写接口 403**；种子模板门禁/审查阶段的 agent_selector ≠ 上游 executor（publish 时校验）
- R4（P0-4）：Submission + Verdict 实体与 API；submission 含 status 四态（DONE/DONE_WITH_CONCERNS/BLOCKED/NEEDS_CONTEXT）+ gaps（清单 D-2）；准出 schema 随 template_snapshot 冻结，handler+service 双层校验，结构化 field 级错误
- R5（P0-5）：Acceptance 机制：End 节点 → 通知（inbox + Lark/Slack 复用）→ approved/rejected；rejected 定向返工 + rework_context（daemon 显式注入 prompt，清单 D-8；注入前消毒）；auto_pass 为节点能力默认关（D-12 裁决）
- R6（P0-6）：入站 Hook：POST（标题/描述/负责人/来源链接/template_key）→ 同事务创建 intake 父 issue + Run；幂等（重复推送返回已有 Run，UNIQUE(workspace_id, source_type, source_id, template_id) 硬约束）；**新增第 10 张表 workflow_hook 存 token（SHA-256 hash）+ 限流；delivery 审计 P0 降级为 last_used_at + 结构化日志；reviewer 字段接受 member id 或 email，解析失败 400**
- R7（P0-7）：模板管理 UI（表单式）+ Run 列表/详情页（Step 状态、Submission、Verdict、step_transition 时间线）
- R8（P0-8）：双模板种子：standard（7 阶段含独立 API 门禁，以 squad-briefing 为准，D-1；**门禁/审查阶段在 P0 映射为 evaluator 角色 agent 节点**，P1 升级 gate 类型）+ bugfix（4 阶段默认人工验收，D-12）
- R9（P0-9）：全部新代码挂 feature flag；触点预算执行（router.go +1 行、main.go +2 行、packages/core/types/events.ts 扩展事件类型）；合并 playbook 演练一次
- R10（TS-4 P0 部分）：CLI 扩展 `multica submission create`（只写提交）/ `multica verdict create`（仅 evaluator 角色 token）/ `multica verdict get` / `multica step context`（mat_ token 鉴权）

## Acceptance Criteria（= roadmap P0 出口标准，可测）

- [ ] AC1: 一条模拟标准需求经 Hook 进入后，依次走完评审→分发→实现→验收 4 节点（4 节点测试模板），全程无人手动转派，最终负责人收到完成通知
- [ ] AC2: 负责人在 UI 驳回验收并指定"实现"节点：①实现 StepInstance 以新 attempt 重建且 Agent 上下文可见 rework_context；②下游已 passed 的门禁/审查节点被置 skipped 并随推进**重新执行**；③流程重新到达验收
- [ ] AC3: Agent 提交的 Submission 缺少节点声明的必填准出字段时，系统拒绝并返回结构化错误（不进入 verdict）
- [ ] AC4: 每个 Step 的 Issue/AgentTask/Submission/Verdict/step_transition 在 Run 详情页可完整追溯
- [ ] AC5: `make check` 全量通过；上游合并一次演练成功（冲突 ≤ 触点预算）
- [ ] AC6: 全部新代码在 feature flag 关闭时对现有行为零影响（回归证据）
- [ ] AC7: 种子模板可实例化且结构正确：standard 9 节点（含 Spec Freeze acceptance + 独立 end）、bugfix 6 节点；以种子模板各跑通一次完整 Run
- [ ] AC8: Hook 契约：①同一 source_type+source_id+template_id 重复推送返回 200 + 已有 run_id，不重复创建；②reviewer 非法（非 member id/email）返回 400
- [ ] AC9: CLI 契约：executor token 调 `verdict create` 返回 403；evaluator token 可写；`step context` 输出节点 instructions + 上游 exit_fields + 本节点 schema

## Non-Goals（P1/P2 内容，本任务不做）

- gate 节点五形态、fan_out/converge、条件路由（JSONLogic 求值）、Rules 资产、能力匹配路由、workflow 级 Sweeper、失败诊断视图（P1）
- event_store、出站 webhook、指标大屏、知识沉淀池（P2）
- 节点画布（P3）；Temporal（P3 预案）

## 波次划分（详见 implement.md）

- Wave 1 地基：R1 → R2 → R3/R4
- Wave 2 链路：R5 → R6 → R10
- Wave 3 可见：R7 → R8 → 端到端验证（AC1-AC6）+ R9
