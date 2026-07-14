# Methodology Ownership

当某条 workflow、skill、门禁或智能体指令可能同时属于多个方法论层时，使用本指南。

## 归属矩阵

| 层 | 主要 owner | 用于 | 不要用于 |
| --- | --- | --- | --- |
| Tencent harness 实践 | 外部工程证据 | 失败模式、人/脚本门禁启发式、软疤可见性、项目记忆教训 | 仓库运行时、安装，或在无本地目标时照搬 Tencent 内部基础设施 |
| Superpowers | 智能体工作方法 | TDD、系统化调试、独立 review | Trellis 规划、task 状态、项目持久化、平台生成、evidence schema、完成门禁 |
| Trellis | workflow 与持久化框架 | brainstorm、PRD/design/implementation 规划、task 生命周期、`.trellis/` 状态、context 注入、质量与完成门禁、平台适配器、init/update/migration 安全 | 第三方方法论 bootstrap、业务门禁、项目特定的发布契约 |
| AI Native Dev Process | 项目集成与治理 | executor 协议、evidence runtime、`release-plan`、marketplace 资产、Tencent 适配、可选的 Orca 集成 | 通用上游方法论的分叉副本，或第二套 Trellis 生命周期 |

## 集成规则

1. Trellis task 状态与 artifact 是唯一的生命周期账本。不要在它们旁边再加 Superpowers 进度账本。
2. Trellis `trellis-check` 仍是可修复的项目质量门禁。Superpowers 独立 review 仍是另一种独立的只读 review 方法。
3. 用 Trellis `trellis-brainstorm` 作为 `prd.md`、`design.md`、`implement.md` 的唯一 owner；不要另建并行的 Superpowers plan 目录。
4. 在 Trellis Execute 内部应用 Superpowers 的 TDD 与调试方法；它们不改 task 状态，也不绕过 Trellis 门禁。
5. 只有当本仓库有真实目标和验证命令时，才把 Tencent 教训翻译成可执行的本地脚本/spec。
6. 拉取源码不等于安装：`vendor/superpowers` 在官方 harness bootstrap 发现它之前只是审计输入。
7. 确定性 worker 不得接收隐式的项目级记忆。`trellis mem` 是显式检索工具；召回的文本在被接受进 task/spec artifact 之前仍是 source evidence。
8. reviewer/planner 权限应遵循最小权限。只读方法不会仅因为 harness 能提供，就获得写/发布权限。
9. 外部模型、网络或发布调用需要 fixture/mock 边界，以便能在不付出昂贵或不可逆步骤的情况下测试编排。
10. worker 的 `DONE`/`BLOCKED` 报告是结构化的交接证据，不是生命周期 status；Trellis task 状态才是权威。
11. 业务 Case 生成是对 Superpowers 代码级 TDD 的补充。冻结的业务期望在实现前 review；技术 Case 与 RED-GREEN 测试在实现期间产出。
12. 并行执行以显式依赖和不相交的改动为条件。Join 操作保持受监督且串行。
13. 具名的第三方智能体绝不是可移植的硬依赖。只通过能力检测选择它们；否则用同一角色契约，走可用的 reviewer/worker 或文档化的 inline fallback。

## 决策示例

| 需求 | owner | 本地边界 |
| --- | --- | --- |
| 提高价值的需求问题并写实施计划 | Trellis brainstorm | Trellis 拥有收敛后的 `prd.md`、`design.md`、`implement.md` 与批准门禁 |
| 决定实现是否可以开始 | Trellis | 用户 review 加 `task.py start` |
| 记录确定性门禁证据 | AI Native Dev Process | `gate-result.jsonl` 是证据，不是生命周期状态 |
| 防止反复的调试失败 | Superpowers 系统化调试，然后 Trellis break-loop | 先根因；可复用教训落入 `.trellis/spec/` |
| 让 warning 可见 | 由本项目翻译的 Tencent 证据 | `scar_summary.py`；不新增状态机 |
| 生成平台 hook 与 Trellis skill | Trellis npm CLI | `trellis init/update`，绝不走 Trellis 源码子模块 |
| 回忆一段先前的对话 | Trellis session insight | 仅显式查询；把接受的结论持久化进当前 artifact |
| 测试一条长编排链 | AI Native Dev Process | mock 外部生成/发布；在本地断言状态转换与回滚 |
| 独立 review 一个 plan 或稳定 diff | Superpowers review 方法 | 只读角色汇报证据；Trellis checker 或 implementer 执行修复 |
| 把需求追溯到执行 | AI Native Dev Process | `verification-contract.json` 加 append-only Case 证据；不新增生命周期状态 |

## 变更前检查

新增一条 workflow 指令或 skill 之前：

- 从矩阵里精确确定唯一一个主要 owner；
- 在上游 Trellis 和 Superpowers 源里搜索既有行为；
- 选定项目集成边界（若有）；
- 定义一个可执行的验证信号；
- 若该改动会产生重复状态、reviewer 角色或 bootstrap 路径，则拒绝。
