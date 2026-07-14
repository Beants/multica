# Plan Artifact Contract

> Plan / Spec Freeze artifact 的文件状态归属。写或接受规划 artifact 时使用本指南。

## 原则

用 Trellis 文件作为耐用的状态面。在 Code CLI 把接受的结论写进 task artifact 之前，不要把聊天记忆、worktree 元数据、异步 worker 运行日志，或外部记忆/eval 报告当作 canonical 规划状态。

## Owner

| Artifact | Owner | 用途 |
|---|---|---|
| `.trellis/tasks/<task>/task.json` | Trellis | task 生命周期 |
| `.trellis/tasks/<task>/plan-state.yaml` | Code CLI | Plan / Spec Freeze dashboard |
| `.trellis/tasks/<task>/branch-base-contract.yaml` | Code CLI | 稳定分支与 MR 策略 |
| `.trellis/tasks/<task>/source-index.md` | Code CLI | source 输入覆盖 |
| `.trellis/tasks/<task>/prd.md` | Code CLI | 归一化的需求 |
| `.trellis/tasks/<task>/design.md` | Code CLI | 技术方案与契约 |
| `.trellis/tasks/<task>/implement.md` | Code CLI | 执行计划与验证 |
| `.trellis/tasks/<task>/task-map.md` | Code CLI | 父/子拆解 |
| `.trellis/tasks/<task>/decision-log.md` | Code CLI | 人工决策与延期 |
| `.trellis/tasks/<task>/open-questions.md` | Code CLI | 真正需要人的问题 |
| `.trellis/tasks/<task>/acceptance-matrix.md` | Code CLI | 需求到测试的映射 |
| `.trellis/tasks/<task>/impact-map.md` | Code CLI | 代码/数据/接口影响 |
| `.trellis/tasks/<task>/research/` | Code CLI | 调研过的未知项 |
| `.trellis/tasks/<task>/reviews/` | Code CLI | review 过的 advisory 输出 |
| `.trellis/tasks/<task>/eval/` | Code CLI 接受自外部报告或直接创建 | 长期 eval 报告 |
| `.trellis/tasks/<task>/gate-result.jsonl` | helper append，Code CLI 消费 | evidence runtime v0 事件 |
| `.trellis/tasks/<task>/baseline/{before,after,diff}.json` | helper 写，Code CLI 消费 | baseline 回归证据 |

## status 规则

`plan-state.yaml.status` 只用于规划状态：

```text
intake | planning | needs_human_decision | drafting_artifacts | self_checking | ready_for_spec_freeze | spec_frozen | blocked
```

不要在 `plan-state.yaml` 里重复 Trellis 生命周期状态，如 `in_progress`、`completed`、`archived`，或交付就绪。

`task.json.status` 是生命周期的唯一来源：
- `planning`（由 `task.py create` 设置）
- `in_progress`（由 `task.py start` 设置）
- `completed`（由 `task.py archive` 设置）

自定义 status（如 `blocked`、`in-review`）只是 `task.json.status` 里的普通字符串；breadcrumb 系统通过 `workflow.md` 中的 `[workflow-state:CUSTOM]` 块支持它们。

## Source 文档

把原始 PRD、会议纪要、Jira 导出、API 文档、数据样本和 eval 报告留在项目 `docs/` 或外部系统里。

把引用写进 `source-index.md`：

- source id（如 `SRC-001`）
- 类型（prd / meeting / jira / upstream_contract / eval_report / other）
- 路径或 URL
- owner
- 日期
- 覆盖值（`pending | covered | partial | deferred | rejected`）
- 被消费的 artifact
- 备注

只在 review 需要稳定引用时使用短证据摘录。

## Case 模式（主要产出形态）

选一种主要模式。次要模式可加检查，但主要模式决定 artifact 结构和派发策略。

| 模式 | 何时使用 | 主要产出 |
|---|---|---|
| `release_planning` | PRD、会议纪要、多周 release、大量 story | 父 plan、子 task map |
| `jira_triage` | Jira 缺陷、支持问题、生产现象 | 可复现的问题陈述与修复 task |
| `contract_change` | 上游字段/API/schema/事件/权限变更 | 接口影响与迁移计划 |
| `data_quality` | 指标不匹配、报表错误、对账失败 | 数据血缘与验收矩阵 |
| `agent_regression` | AI workflow、prompt、工具或智能体行为变差 | 回归假设与 eval 计划 |

## Intake 问题

写 artifact 之前，尽量从 source 回答：

- 业务结果是什么？
- 变化了什么，或期望什么新行为？
- 影响哪些用户、租户、地区、角色或 workflow？
- 涉及什么系统边界？
- 显式排除的是什么？
- 存在哪些证据？
- 哪些必须由人决定？

## 子 task 规则

只有当父 plan 具备以下条件后才创建子 task：

- 稳定的 source index
- 清晰的验收切片
- 接口/数据边界
- 依赖顺序
- 验证策略

不要只按文件或层拆 task。尽量按可独立 review 的用户/业务结果拆。当一个请求包含多个可独立验证的交付物时用父 task；当交付物能被独立规划、实现、检查和归档时用子 task。

## 写入纪律

接受 advisory 输出（异步 worker、外部记忆/eval 报告）时：

1. 读原始结果或 MR。
2. 决定接受、拒绝还是延期。
3. 把决策记进 `reviews/` 或 `decision-log.md`。
4. 更新 canonical artifact。
5. 更新 `plan-state.yaml.readiness` 与异步结果清单。

不要让异步 worker、外部记忆/eval 报告或评论默默变成事实。

## Spec Freeze

Spec Freeze 是人工确认：规划 artifact 已足够自洽，可以开始实现。门禁清单见 [Evidence and Quality Gates](./evidence-and-quality-gates.md)。

### 前置条件

询问用户之前：

- `plan-state.yaml.status` 为 `ready_for_spec_freeze`。
- `plan-state.yaml.readiness.self_check_passed` 为 true。
- `source-index.md` 没有未解释的 pending source。
- `plan-self-check.md` 通过。
- 若派发了异步 worker，所有 inbox 项都已接受、拒绝或延期。
- 人工决策已解决或被 owner 显式延期。
- 使用基于分支的协作时，分支策略把子工作指向 `plan/<slug>`。

### 确认之后

1. 把 `plan-state.yaml.status` 设为 `spec_frozen`。
2. 把 `plan-state.yaml.readiness.spec_freeze_confirmed` 设为 true。
3. 在 `decision-log.md` 记录确认。
4. 运行 Trellis `task.py start` 或项目批准的等价命令。

不要让异步 worker 或外部记忆/eval 报告标记 Spec Freeze。

### 重开

冻结后若出现新 source 输入：

- 记进 `source-index.md`
- 判断它是否使 PRD、design、implement 或 task-map 失效
- 只有当验收、架构或 task 边界变化时才重开 Plan
- 在 `decision-log.md` 记录重开原因
