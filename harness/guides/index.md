# AI Native Dev Process — 指南

> **目的**：配合 Trellis 进行 AI 原生开发的项目级工程指南。AI 在写代码前先读
> 这些，从而在项目的契约内工作，而不是自己发明。

## 可用指南

| 指南 | 用途 | AI 何时读 |
|---|---|---|
| [Executor Protocol](./executor-protocol.md) | fix/debug/planning-late turn 中 AI 执行者的行为契约 | 任何 fix 型 task、debug 会话、或决策数 > 5 时 |
| [Evidence and Quality Gates](./evidence-and-quality-gates.md) | spec、source、架构、拆解、测试、review、反糊弄、证据响应、密钥、context 更新的 10 道门禁 | Spec Freeze 前、实现后、声明完成前 |
| [Plan Artifact Contract](./plan-artifact-contract.md) | Plan / Spec Freeze artifact 的文件归属 | 写或接受规划 artifact 时 |
| [Baseline and Gate-Result Protocol](./baseline-and-gate-result-protocol.md) | `gate-result.jsonl` 与 `baseline/{before,after,diff}.json` 的 v0 schema | 产出或消费 evidence 文件时 |
| [Dashboard Evidence Contract](./dashboard-evidence-contract.md) | dashboard 消费就绪度、证据、attention 用的 TypeScript 接口 | 实现 dashboard scanner 或 UI 时；修改 evidence schema 时 |
| [Traceable Harness Contracts](./traceable-harness-contracts.md) | 可选的 Requirement/Case/evidence、只读角色、条件性 DAG/Join 契约 | 复杂或高风险的规划与执行 |
| [Script-First Architecture](./script-first-architecture.md) | 把确定性执行与语义模型判断分开 | 设计 gate、脚本或长链路 workflow 时 |
| [Methodology Ownership](./methodology-ownership.md) | 保持 Trellis、Superpowers、外部 evidence、项目治理职责清晰 | 增删 workflow/skill 行为时 |
| [Tool Interaction Contract](./tool-interaction-contract.md) | canonical 状态、advisory 结果、权限、跨工具写入边界 | 集成 Orca、Git/MR 或外部报告时 |
| [Trellis Extension Points](./trellis-extension-points.md) | 支持的 lifecycle hook、平台 hook、技能、智能体、更新边界 | 不 fork Trellis 运行时扩展项目时 |

## 速查：何时读什么

### 开始一个 fix 或 debug task

→ 读 [Executor Protocol](./executor-protocol.md)。先讲现象再讲范围。每条论断带证据
等级。下环境结论前先有请求矩阵。切换环境必须 human gate。

### 请求 Spec Freeze 前

→ 读 [Evidence and Quality Gates](./evidence-and-quality-gates.md)。10 道门禁必须
全过或 owner 延期。读 [Plan Artifact Contract](./plan-artifact-contract.md)——每个
artifact 只有一个 owner；不要重复表达生命周期状态。

### 产出或消费证据

→ 读 [Baseline and Gate-Result Protocol](./baseline-and-gate-result-protocol.md)。
硬门禁阻断；软门禁留疤。baseline diff 只对 `new_failures` 阻断，从不对
`known_failures` 阻断。

### 修改 dashboard 或 evidence schema

→ 读 [Dashboard Evidence Contract](./dashboard-evidence-contract.md)。scanner 对缺失
文件容忍为 `unknown`，不是 `failed`。破坏性变更要 bump `schema`。

### 规划可追溯性或并行工作

→ 读 [Traceable Harness Contracts](./traceable-harness-contracts.md)。保持轻量任务
轻量；共享/全局改动和所有 Join 操作走串行。

## 核心原则

30 分钟的思考能省 3 小时的调试。这些指南存在的意义，是让 AI 不跨会话重复同一类
错误。
