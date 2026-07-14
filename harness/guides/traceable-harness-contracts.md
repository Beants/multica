# Traceable Harness Contracts

对复杂/高风险 task 可选使用的契约。Trellis 仍是唯一的 task 生命周期；Case 文件与 worker 报告是证据。

## 可追溯性

```text
Requirement -> Business Case -> Technical Case -> Automated Test -> Evidence
```

- 使用稳定的 `REQ-*`、`BC-*`、`TC-*` ID。
- 在实现之前 review 并冻结可观测的业务期望。
- 冻结的期望变化时重开并递增 spec revision。
- 在实现期间派生技术 Case 与 RED-GREEN 测试。
- diff 稳定后独立执行适用的 P0/P1 Case。
- 把 design、automation、execution、pass 和 evidence 覆盖分开。
- 不适用的定义和阻塞/跳过的执行结果必须给原因。

`acceptance-matrix.md` 是人工叙述。有运行时 helper 的项目可用 `verification-contract.json` 和 append-only 的 `case-results.jsonl` 做机器验证。只读 verifier 返回事件；supervisor 校验并 append。spec registry 不安装这些运行时脚本。

## 角色

- implementer 写代码/测试，但不写冻结的业务期望；
- Trellis checker 可修复项目质量问题；
- plan reviewer 与验收 verifier 只读，不写账本；
- supervisor 拥有生命周期状态、校验后的 evidence append，以及人工批准的 Join 操作。

worker 报告 `DONE`、`DONE_WITH_CONCERNS`、`BLOCKED` 或 `NEEDS_CONTEXT`，并附上改动的文件、确切检查、证据、发现和一条下一步动作。

## 条件性 DAG 与 Join

并行节点声明 `depends_on`、预测的 `touches` 和 `global_changes`。共享入口、协议/schema、数据库/配置和发布状态串行执行。merge、publish、最终验证和 commit 是 Join 阶段操作。轻量 task 不创建空的 DAG 或 Case artifact。

## 记忆与外部边界

使用显式的 task/spec 输入，而非隐式全局记忆。把显式历史召回当作证据，直到被持久化。测试编排时 mock 外部模型、网络和发布边界。
