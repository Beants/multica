# Multica Issue #1943: Workflow Orchestration

- 原文：https://github.com/multica-ai/multica/issues/1943
- 提交者：CyborgYL
- 开启时间：2026-04-30
- 归档日期：2026-07-14

---

## 需求

希望 Multica 支持内置 Workflow 编排能力，用于更可控地管理多 Agent 协作流程。

### 背景

Multica 目前已将 Agent 作为一等公民，但缺少 workflow 编排层来控制多个 Agent 任务之间的执行顺序、条件分支、审批节点、失败重试和状态流转。

### 当前方案及问题

1. 使用 PMO / Coordinator Agent Skill 维护流程状态机 — skill 约束不够强，依赖 prompt
2. 使用轮询机制控制任务流转 — 实现复杂，效率低
3. 借助 n8n、飞书工作流等外部工具 — 跨服务配置复杂

### 期望能力

- 节点配置：绑定 Agent / Skill / CLI 命令 / Webhook / 人工审批
- 条件网关：if/else、审批通过/不通过、测试通过/失败
- 状态机约束：流程状态由 workflow 引擎控制，不依赖 Agent 自觉
- 多 Agent 协作：不同 Agent 在不同节点承担明确职责
- 失败处理：retry / fallback / timeout / 人工接管
- 可视化追踪：当前执行到哪个节点、谁在处理、为什么阻塞
- 与 Issue 集成：绑定到 issue / project / template / workspace
- 事件触发：issue 创建、状态变更、评论、Agent 输出、CI 结果

### 价值

> 让 Agent 协作不只是靠对话和约定，而是由可视化、可审计、强约束的 workflow 来驱动。
