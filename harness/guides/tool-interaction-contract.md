# 工具交互契约

> 日期：2026-07-01
> 目的：在生成 `templates/` 之前，先固定 Trellis、Code CLI、Orca、Git/MR 之间的原生输入输出、状态归属和文件写入边界，并约束外部记忆/评估报告如何进入 Trellis。

---

## 1. 核心原则

模板不是新的系统状态机。模板只把各工具已经存在的原生信息用文件化方式串起来。

工具接入先看协议对象，而不是工具名称。本指南下文直接定义每个对象的
canonical owner、允许写入者和跨工具传递方式，不依赖发布仓库之外的文档。

必须遵守：

- Trellis 是 canonical task/spec/workflow 文件系统。
- Code CLI 是当前需求的人工交互入口、状态合并者和 gate keeper。
- Orca 只拥有本地 worktree / terminal / workspace lifecycle。
- 不假设存在可由 Code CLI 直接调用的“龙虾智能体”链路；长期记忆/评估只作为外部报告或人工提供的 source input 进入 Trellis。
- Git remote `plan/<slug>` 是跨工具协作共享点，本地 worktree 不是共享点。

一句话：

```text
Trellis files hold durable state.
Code CLI mutates canonical planning artifacts.
Orca creates the local control worktree.
External memory/eval reports are imported as source inputs only after Code CLI review.
```

---

## 2. 原生信息边界

### 2.1 Trellis

原生输入：

- 用户同意创建 task。
- `.trellis/workflow.md` 的阶段规则。
- `.trellis/spec/` 的项目规范。
- `.trellis/tasks/<task>/task.json`。
- `.trellis/tasks/<task>/prd.md`、`design.md`、`implement.md`。
- `implement.jsonl`、`check.jsonl`。
- 父子任务命令：`task.py create --parent`、`task.py add-subtask`、`task.py remove-subtask`。

原生输出：

- task lifecycle：`planning`、`in_progress`、`completed` 等写入 `task.json`。
- 当前阶段和下一步由 Trellis workflow/context 推导。
- 任务 artifacts 和 spec guidelines。

契约：

- `task.json` 是任务生命周期事实来源。
- `plan-state.yaml` 只做 Plan / Spec Freeze dashboard，不重复表达执行和交付生命周期。
- `prd.md` 放需求、边界、约束、验收标准。
- `design.md` 放技术设计、数据流、接口契约、风险、回滚。
- `implement.md` 放执行拆解、验证命令、review gate。
- 父任务拥有源需求、任务地图、跨子任务验收和集成就绪。
- 子任务使用 Trellis 原生 task 目录，保持可独立实现和验证。

### 2.2 Code CLI

原生输入：

- 用户对话。
- 当前工作目录中的 repo、branch、diff。
- Trellis workflow/context 输出。
- Orca 创建的 control worktree。
- 外部提供的历史召回、评估报告或复盘材料。

原生输出：

- Trellis artifacts 的实际修改。
- `plan-state.yaml` 状态推进。
- 对 child MR 的 review、merge、reject 决策。
- 对用户的人类决策问题。

契约：

- Code CLI 是唯一可以把 advisory result 写成 canonical planning state 的执行者。
- Code CLI 可以编辑 `plan-state.yaml`、`branch-base-contract.yaml`、`source-index.md`、`prd.md`、`design.md`、`implement.md`、`task-map.md`、`decision-log.md`、`open-questions.md`、`acceptance-matrix.md`、`impact-map.md`。
- Code CLI 不拥有 worktree lifecycle；worktree 创建/打开归 Orca。

### 2.3 Orca

原生输入：

- repo selector：`path:<repo>`、`id:<repoId>`、`name:<name>` 等。
- worktree name：`--name <name>`。
- base branch/ref：`--base-branch <ref>`。
- agent：`--agent codex` 等。
- prompt：`--prompt <text>`。
- setup policy：`--setup run|skip|inherit`。
- lineage：`--parent-worktree <selector>` 或 `--no-parent`。

原生输出：

- Orca-managed worktree。
- Orca terminal。
- Orca metadata，包括 parent/child worktree lineage。
- 可选的 setup hook 执行结果。

契约：

- Orca 创建/打开 control worktree，但不决定 Trellis task 状态。
- Orca setup hook 可以调用初始化脚本，但脚本写入的 Trellis artifact 仍由 Code CLI 负责审核。
- 模板中可以记录 Orca command、worktree path、setup policy、parent worktree，但不要把 Orca metadata 当 task state。

命令形态：

```bash
orca worktree create \
  --repo path:/path/to/project \
  --name feature-plan \
  --base-branch origin/main \
  --agent codex \
  --prompt "Initialize the file-based Trellis Plan workflow for feature-plan." \
  --setup inherit \
  --json
```

### 2.4 外部长期记忆 / 评估产物

当前不定义“龙虾智能体”的可调用原生接口。除非后续单独验证 CLI/API/调度链路，否则它不能出现在默认执行链路中。

可接受的输入形态：

- 人工提供的历史召回报告。
- 外部系统导出的 eval report。
- release retro 文档。
- recurring failure 分析文档。
- spec/workflow 改进建议文档。

契约：

- 不假设 Code CLI 可以直接调用外部记忆 agent。
- 不假设外部系统可以直接写项目文件。
- 外部产物必须先写入或引用到 `source-index.md`。
- 只有 Code CLI 审核接受后，结论才进入 `research/`、`reviews/`、`eval/` 或 `.trellis/spec/` 候选更新。

---

## 3. Canonical / Advisory 划分

| 信息 | Canonical owner | 允许修改者 | 消费方 | 说明 |
|---|---|---|---|---|
| task lifecycle | Trellis `task.json` | Trellis command / Code CLI invoking command | Code CLI、agents | 不在 `plan-state.yaml` 重复维护 |
| Plan dashboard | `plan-state.yaml` | Code CLI | Code CLI | 只覆盖 Plan / Spec Freeze |
| PRD | `prd.md` | Code CLI | Trellis | 源需求的规范化版本 |
| Design | `design.md` | Code CLI | Coder、Reviewer | 技术契约和影响面 |
| Implementation plan | `implement.md` | Code CLI | Code CLI | 执行路径和验证命令 |
| Source docs | project `docs/` | 人类 / Code CLI | Trellis | 大型原始文档不复制进 task；外部评估报告也按 source doc 处理 |
| Source index | `source-index.md` | Code CLI | Code CLI | 引用 PRD、会议、Jira、接口文档、外部评估报告 |
| Runtime facts | task context / notes / optional `runtime-facts.md` | Code CLI | Code CLI、Trellis | 运行期事实、环境事件、用户纠偏；可选日志，不是长期记忆 |
| Decision log | `decision-log.md` | Code CLI | 所有工具 | 人类 gate 的持久记录 |
| Open questions | `open-questions.md` | Code CLI | Code CLI、人类 | 只保留必须人类决策的问题 |
| MR metadata | Git platform | Git platform / Code CLI | Code CLI | source/target/url/conflict 状态 |
| Orca worktree metadata | Orca | Orca | Code CLI | 不等同 task state |
| Long-term eval | `eval/*.md` or `.trellis/spec/*` candidate | external report or Code CLI creates, Code CLI accepts | Code CLI、Trellis | advisory until reviewed |

---

## 4. 文件写入权限

### 4.1 用户输入类型

OpenCode 历史输入显示，大量用户消息不是正式需求，而是执行期 micro-turn。Code CLI 应先分类再写入正确 artifact：

| 输入类型 | 示例 | 写入位置 | 是否 human gate |
|---|---|---|---:|
| `user_fact` | 服务已重启、token 在 `.env`、网关映射、字段含义 | task context / notes；事实多时用可选 `runtime-facts.md` | 否 |
| `user_correction` | “不是这个意思 / 搞错了 / 应该是...” | task context / notes；必要时更新 `prd.md` / `design.md` | 否 |
| `human_decision` | 业务取舍、scope trade-off、权限/安全边界 | `decision-log.md` / `open-questions.md` | 是 |
| `operator_command` | 继续、测试、review、提交、推送、收尾 | 对应执行协议；证据写 reviews / task note / commit record | 否 |

规则：

- 不把 `user_fact` 和 `user_correction` 自动升级为 `needs_human_decision`。
- 只有无法由 agent 自行查证、且需要业务/权限/范围取舍的问题才进入 human gate。
- 短控制命令如果开始跨多轮、涉及文件修改、测试或提交，应升级为 Trellis task 或写入当前 task 的执行记录。

### 4.2 Code CLI 可写

```text
.trellis/tasks/<parent-task>/plan-state.yaml
.trellis/tasks/<parent-task>/branch-base-contract.yaml
.trellis/tasks/<parent-task>/source-index.md
.trellis/tasks/<parent-task>/runtime-facts.md  # optional, fact-heavy tasks only
.trellis/tasks/<parent-task>/prd.md
.trellis/tasks/<parent-task>/design.md
.trellis/tasks/<parent-task>/implement.md
.trellis/tasks/<parent-task>/task-map.md
.trellis/tasks/<parent-task>/decision-log.md
.trellis/tasks/<parent-task>/open-questions.md
.trellis/tasks/<parent-task>/acceptance-matrix.md
.trellis/tasks/<parent-task>/impact-map.md
.trellis/tasks/<parent-task>/research/
.trellis/tasks/<parent-task>/reviews/
.trellis/tasks/<parent-task>/eval/
```

### 4.3 Orca 可写

Orca 写的是 worktree/terminal/workspace 层面的元数据和 checkout，不写 task state。

允许：

- 创建 worktree。
- 运行 setup hook。
- 启动 Code CLI terminal。
- 记录 worktree lineage。

不允许：

- 直接推进 Trellis task lifecycle。
- 直接合并 child MR。

### 4.4 外部评估产物导入

外部记忆/评估系统默认不直接写项目文件。若人工或已验证集成提供产物，Code CLI 先作为 source input 登记，再按需导入候选区：

```text
.trellis/tasks/<task>/research/memory-recall.md
.trellis/tasks/<task>/reviews/history-risk-review.md
.trellis/tasks/<task>/eval/trend-review.md
```

写回 `.trellis/spec/` 需要 Code CLI review。

---

## 5. 模板字段来源映射

### 5.1 `plan-state.yaml`

| 字段 | 来源 | 修改者 | 说明 |
|---|---|---|---|
| `version` | 模板版本 | Code CLI | 模板 schema 版本 |
| `mode` | Code CLI 根据任务类型设置 | Code CLI | release_planning / jira_triage / contract_change 等 |
| `owner` | 固定为 `code_cli` | Code CLI | 表达状态 owner，不是 assignee |
| `status` | Plan workflow | Code CLI | 只允许 Plan / Spec Freeze 状态 |
| `branch_base_contract.canonical_branch` | Code CLI / git branch | Code CLI | 通常为 `plan/<slug>` |
| `branch_base_contract.final_target` | repo policy | Code CLI | 通常为 `main` |
| `source_inputs` | `source-index.md` 摘要 | Code CLI | 保持短引用，不复制全文 |
| `stages` | Plan workflow | Code CLI | dashboard，不代替 task status |
| `human_decisions` | `open-questions.md` / `decision-log.md` | Code CLI | 可存摘要和路径 |
| `readiness` | artifact review | Code CLI | Spec Freeze gate |

### 5.2 `source-index.md`

| 字段 | 来源 | 修改者 | 说明 |
|---|---|---|---|
| source id | Code CLI | Code CLI | 稳定短 ID |
| type | 文档类型 | Code CLI | PRD / meeting / Jira / API / eval |
| path/url | 项目 `docs/` 或外部链接 | Code CLI | 不复制大文档 |
| owner | 人类/系统 owner | Code CLI | 用于问题回流 |
| coverage | Problem Analysis | Code CLI | covered / partial / deferred |
| consumed_by | artifact 路径 | Code CLI | 指向 prd/design/impact 等 |

### 5.3 `branch-base-contract.yaml`

| 字段 | 来源 | 修改者 | 消费方 |
|---|---|---|---|
| `canonical_branch` | Code CLI / git branch | Code CLI | Code CLI |
| `repo.control_worktree_base_ref` | Orca `--base-branch` / Code CLI 参数 | Code CLI | Code CLI；只表示 control worktree 创建来源 |
| `final_target` | repo policy | Code CLI | Code CLI |

---

## 6. 状态边界

### 6.1 Trellis task status

归属：Trellis `task.json`。

表达：

- task 是否在 planning。
- task 是否已经 start / in_progress。
- task 是否 completed / archived。

禁止：

- 不在 `plan-state.yaml` 维护第二套 `in_progress` / `delivered`。

### 6.2 Plan state status

归属：`plan-state.yaml`。

允许：

```text
intake
planning
needs_human_decision
drafting_artifacts
self_checking
ready_for_spec_freeze
spec_frozen
blocked
```

表达：

- Plan artifacts 是否准备好。
- 是否需要人类决策。
- 是否到 Spec Freeze gate。

禁止：

- 不表达代码是否已交付。
- 不表达 MR 是否已上线。
- 不表达 task 是否 archived。

---

## 7. 交互流

### 7.1 从需求到 Plan

```text
User gives PRD / meeting / Jira / contract change
Code CLI classifies and asks Trellis task consent when needed
Orca creates/opens control worktree
Code CLI creates Trellis parent task after consent
Code CLI writes source-index.md, branch-base-contract.yaml, and initial plan-state.yaml
Code CLI normalizes PRD/design/implement/task-map
```

### 7.2 导入外部长期记忆 / 评估产物

```text
Human or verified external integration provides historical recall / trend review
Code CLI records the report in source-index.md
Code CLI stores accepted summaries under research/reviews/eval
Code CLI extracts accepted decisions/spec candidates
```

---

## 8. 冲突和阻塞协议

| 场景 | 检测者 | 输出 | 决策者 |
|---|---|---|---|
| MR target 错误 | Code CLI reviewer | fail | Code CLI |
| MR 冲突 | Git platform | pr_conflicted | Code CLI |
| 人类业务歧义 | Code CLI advisory | needs_human_decision | 用户 |
| 权限/安全边界 | Code CLI advisory | needs_human_decision | 用户 |
| scope trade-off | Code CLI | needs_human_decision | 用户 |
| 长期风险 | external eval source | advisory report | Code CLI |

规则：

- Code CLI 可以选择 rebase、手工合并或关闭 MR。
- 人类 gate 只处理 agent 无法自行查证的问题。

---

## 9. 模板生成约束

后续 `templates/` 生成时必须满足：

- 每个模板都标注 `owner`、`producer`、`consumer`。
- 每个状态字段都标注是否 canonical。
- `plan-state.yaml` 不包含 Trellis task lifecycle。
- `source-index.md` 只引用项目 `docs/` 或外部链接，不复制大型源文档。
- `plan-self-check.md` 必须检查 source coverage、human decisions、branch contract、Spec Freeze readiness。

推荐模板清单：

```text
templates/
├── plan-state.yaml
├── source-index.md
├── branch-base-contract.yaml
└── plan-self-check.md
```

---

## 10. 暂不模板化的内容

以下信息不放入第一版模板，避免过早绑定平台细节：

- Orca worktree id。
- Orca terminal id。
- 具体 GitLab/GitHub MR API 字段。
- 外部记忆系统内部 schema。
- Trellis 内部脚本实现细节。

这些信息可以作为运行时 metadata 记录在 result 或日志中，但不作为跨项目模板必填项。
