# Script-First Architecture: 脚本管确定性，Prompt 只留语义判断

> **来源**：Trellis 官方实现分析（当前项目运行时 v0.6.6；`.trellis/scripts/common/`）
> **对应腾讯实践**：《从 Vibe Coding 到 Harness》§2.3，以及《从 AI Coding 到 Harness Engineering》§4.4/§5.1
> **创建**：2026-07-09

---

## 核心原则

**Trellis 的薄 agent prompt（70 行）之所以能工作，是因为 6700 行 Python 把确定性工作全包了。** Prompt 不该承担任何能用脚本 100% 实现的职责。每往 prompt 里加一句"应该做 X"，先问：这个 X 能不能用 exit code 表达？能 → 脚本化。

```
能用脚本 100% 实现 → Script·Gate（exit code 决定下一步）
需要语义判断 → Prompt + Skill（模型解释执行）
两者结合 → 脚本准备上下文，模型做判断
```

### 长链路的实现形态

“脚本化”不等于把复杂状态机塞进临时 Shell。对跨多个步骤、有回滚、结构化状态或外部系统调用的长链路：

1. 优先使用有明确函数/数据类型、可单元测试的 Python/Go/TypeScript 程序；
2. Shell 只保留为稳定命令的薄入口，不承载嵌套状态机或复杂错误恢复；
3. 网络、模型和发布调用必须能替换为 fixture/mock，使调度逻辑可离线快速验证；
4. 每个确定性步骤用返回值、结构化输出和 exit code 表达，不让模型猜执行结果。

这条规则来自应用宝实践中“Agent 驱动 → 外部程序编排”和“复杂 Shell → 强类型 Go 驱动”的演进。项目不强制统一语言，但强制可解析、可测试、可回滚。

---

## Trellis 官方的分层实证

`[已验证]` 本地 `.trellis/scripts/` 实测：

### 第一层：Python 脚本（确定性，模型零参与）

| 模块 | 行数 | 职责 | 如果不用脚本会怎样 |
|---|---|---|---|
| `task_store.py` | 747 | task lifecycle 状态机（create→planning→in_progress→completed→archived） | agent 漏状态翻转、翻转错状态、重复翻转 |
| `active_task.py` | 628 | 跨 12 个平台的 session→task 映射 | agent 不知道"当前是哪个 task"，上下文丢失 |
| `session_context.py` | 821 | context 组装（prd + design + jsonl + git status + packages） | agent 手写"先读 X 再读 Y"prompt，容易漏 |
| `workflow_phase.py` | 212 | 解析 workflow.md 阶段定义 + 平台过滤 | agent 自己判断"现在在哪个 phase"，会判断错 |
| `task_context.py` | 223 | jsonl manifest 注入 sub-agent prompt | agent 不读 spec 文件，违反项目规范 |
| `safe_commit.py` | 315 | git 安全提交 | agent 直接 commit --amend / push / merge |
| Marketplace evidence runtime | — | 激活入口先采 before；交付 checklist 校验证据闭环 | agent 忘记跑 baseline 时无法静默交付 |

**关键洞察**：task lifecycle 的每一步——创建 task.json、翻状态、注入 context、解析 phase——全是确定性代码。模型完全不参与。这就是腾讯说的"能写成 bash 的就别让 Agent 解释执行"——Trellis 把它做到了极致。

### 第二层：Markdown（薄 prompt，模型只做语义判断）

`.trellis/agents/` 只有 2 个文件，各 ~70 行：

| 文件 | 模型做什么 | 模型不做什么（脚本做了） |
|---|---|---|
| `implement.md` | 写代码、跑 lint/typecheck、报告 | 读哪些文件（task_context.py 注入）、task 状态翻转、context 组装 |
| `check.md` | git diff 审查、自修机械问题、报告 | 同上 + spec 文件选择（jsonl manifest） |

**agent prompt 里没有方法论**——只有"读什么、做什么、禁止什么、报告格式"。方法论来自三个外部源：
1. `.trellis/spec/` 项目编码规范
2. `workflow.md` 阶段路由
3. **模型自己的训练知识**（通用编程能力）

---

## 判断标准：该脚本化还是该 prompt 化？

拿到一个需求（比如"需求确认 gate"），按顺序问：

```
1. 能不能用 exit code (0/1/2) 表达结果？
   ├─ 能 → 继续问 2
   └─ 不能（需要语义质量判断）→ Prompt + Skill

2. 输入是结构化的（文件存在/字段存在/jsonl 条目/状态值）吗？
   ├─ 是 → Script·Gate
   └─ 否（需要理解文本含义）→ 继续问 3

3. 能否先用脚本提取/准备输入，再让模型判断？
   ├─ 能 → 混合：脚本准备 + 模型判断
   └─ 不能 → 纯 Prompt
```

### 示例

| 需求 | 判断 | 落地 |
|---|---|---|
| 需求确认 gate（prd.md 存在 + 用户 approve） | exit code 可表达 | ✅ `gate_prd_confirm.py`（结构检查脚本化 + 用户确认记 task.json） |
| 交付收尾 checklist（prd/design/implement/evidence/baseline 齐全） | 全是文件存在性检查 | ✅ `delivery_checklist.py`（纯脚本） |
| 熔断追踪（≥3 次回退） | 次数比较 | ✅ `rollback_counter.py`（纯脚本） |
| 软门禁伤疤汇总（提取 WARN 事件） | jsonl 解析 | ✅ `scar_summary.py`（纯脚本） |
| 流程定义自检（workflow.md 阶段一致） | markdown 解析 | ✅ `workflow_integrity_check.py`（纯脚本） |
| PRD 完整性评分（验收标准是否"清晰"） | 需要语义理解 | ❌ 不能纯脚本。脚本检查结构（有/无 acceptance 段），模型判断质量 |
| 范围溢出检查（diff 是否超出 design.md） | 需要语义理解 | ❌ 脚本提取 changed files vs implement.md 路径，模型判断语义偏差 |
| 代码审查 4 维度收口 | 全是语义判断 | ❌ 纯模型 + Skill（Superpowers requesting-code-review） |

---

## 本项目的脚本清单

### 已有脚本（项目级 `scripts/`）

| 脚本 | 对应腾讯实践 | 脚本化理由 |
|---|---|---|
| `evidence_plan.py` | 验证命令契约固化 | JSON schema 校验 + canonical SHA = 确定性 |
| `evidence_activate.py` | 基线采集前置 | planning 状态、before 顺序、指纹重试 = 确定性 |
| `baseline.py` | 基线对比反作弊 | 失败快照 + diff = 确定性命令 |
| `gate_result.py` | 门禁事件记录 | jsonl append = 确定性 |
| `gate_prd_confirm.py` | ③需求确认〔人〕 | 文件存在性 + 用户确认 = 确定性 |
| `delivery_checklist.py` | ⑬交付收尾 checklist | 文件存在性 = 确定性 |
| `rollback_counter.py` | 熔断暂停 | 次数比较 = 确定性 |
| `scar_summary.py` | 软门禁伤疤 | jsonl 解析 = 确定性 |
| `workflow_integrity_check.py` | 本地/marketplace 流程定义机器可校验 | Phase Index 与实际 sections 的 markdown 解析 = 确定性 |
| `spec_freshness.py` | 规约陈旧度 | 文件日期 = 确定性 |
| `plan_contract_check.py` | 制品一致性 | artifact 存在性 = 确定性 |
| `verification_contract_check.py` | 需求/Case/执行证据追溯 | ID、映射、freeze revision、automation selector 和 current-result 完整性 = 确定性 |
| `task_resolver.py` | 任务定位 | 路径解析、containment guard、archive fallback = 确定性 |

### Trellis 自带脚本（`.trellis/scripts/`，不修改）

| 脚本 | 职责 |
|---|---|
| `task.py` | task lifecycle 状态机 |
| `get_context.py` | context 入口分发 |
| `add_session.py` | 会话追踪 |
| `common/*.py` (20 模块) | active_task / task_store / task_context / session_context / workflow_phase / packages_context / config / ... |

### 宿主工程脚本（IT 同事提供，本项目不管）

| 脚本类型 | 接入方式 |
|---|---|
| lint / typecheck / test | implement.md 的 Validation commands 段，exit code = 门禁信号 |
| 沙箱部署 / 健康检查 | 同上 |
| HTTP 接口测试 | 同上 |
| 前端冒烟（Playwright） | 同上 |
| 覆盖率检查 | 同上 |

宿主工程只需要把真实的 lint、typecheck、test、部署或健康检查命令写入
当前 task 的 `implement.md`；workflow 通过命令退出码和 evidence runtime
记录结果，不依赖发布仓库之外的 gate 集成文档。

---

## 优化时的检查清单

当你要往 prompt/workflow 里加一个"应该做 X"时：

- [ ] X 能用 exit code 表达吗？能 → 先写脚本
- [ ] X 是文件存在性/字段存在性/状态值检查吗？是 → 先写脚本
- [ ] X 已有脚本支撑吗？有 → prompt 里只写"跑这个命令"，不写方法论
- [ ] X 需要语义判断吗？需要 → 脚本准备上下文，模型做判断，方法论委托开源 Skill
- [ ] X 是长链路或外部调用吗？是 → 提供 fixture/mock，禁止测试依赖真实模型或远端平台
- [ ] Shell 是否承载结构化状态机/复杂回滚？是 → 移到可测试程序，Shell 只留薄入口
- [ ] 确认没有在 prompt 里重复脚本已经做的事

**反模式**：在 prompt 里写"检查 prd.md 是否有 acceptance criteria 段"——这是脚本能做的。应该写"跑 `python3 ./.trellis/workflows/ai-native-harness-dev/scripts/gate_prd_confirm.py --task <dir>`，按 exit code 决定"。

**正确模式**：prompt 里只留"用户 approve 后跑 `--confirm`；用户要改则回 Phase 1.1"——这是需要模型判断用户意图的。
