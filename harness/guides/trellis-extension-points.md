# Trellis Extension Points — fork 之前先想想

> **目的**：当你需要让 Trellis 做它开箱即用做不到的事时，按这 5 个 extension point 的顺序尝试。fork Trellis 源码几乎从不是正确答案。

## 本指南为何存在

真实会话里，智能体和人在实际需求本可用 5 个内建 extension point 之一解决时，却去"改 Trellis 源码"或"fork workflow"。本指南是 canonical 的检查清单。

## 5 个 Extension Point（按优先级排序）

### 1. `.trellis/config.yaml` 的 `hooks` 字段 —— task 生命周期 shell 命令

在 task 生命周期事件后触发 shell 命令：

```yaml
hooks:
  after_create:
    - "echo 'Task created'"
  after_start:
    - "python3 scripts/record-phase-metrics.py --event start"
  after_finish:
    - "echo 'Task finished'"
  after_archive:
    - "python3 scripts/record-phase-metrics.py --event archive"
```

每条命令会收到 `TASK_JSON_PATH`。hook 失败只打印 warning，不阻断主 task 操作。

**用于**：evidence/指标记录、通知、归档后同步、由 task 生命周期触发的项目特定簿记。

**`trellis update` 安全**：append-only。新增配置段以注释示例加入；既有值保留。

### 2. 平台级自定义 hook —— SessionStart / UserPromptSubmit / PreToolUse / PostToolUse

支持 hook 的平台（Claude Code、Cursor、OpenCode、Codex、CodeBuddy、Droid、Pi、Gemini、Qoder、Copilot）支持事件 hook。Trellis 内置三个：`session-start`、`inject-workflow-state`、`inject-subagent-context`。可自行添加：

- **`.opencode/plugins/<name>.ts`**（OpenCode JS 插件）
- **`.claude/hooks/<name>.py`**（Claude Code / Cursor / CodeBuddy / Droid Python）
- **`.codex/hooks/`** + `~/.codex/config.toml` `[features] hooks = true`
- **`.pi/extensions/trellis/index.ts`**（Pi extension 形式）

**用于**：拦截工具调用（PostToolUse 在 AI 跑 lint/test 时记录 gate 结果）、注入 context（SessionStart）、修改 prompt（UserPromptSubmit）、门禁工具使用（PreToolUse）。

示例：本项目的 evidence runtime 之后可加一个 `record-gate-result.ts` 插件，监听 Bash 工具调用里的 `lint|test|typecheck` 命令并自动 append 到 `gate-result.jsonl`。

### 3. `.trellis/workflow.md` 直接编辑 —— phase、breadcrumb、skill 路由

Plan → Execute → Finish 的唯一真相源。就地编辑：

- `## Phase Index` 与 `## Phase 1/2/3` 段定义流程
- `[workflow-state:STATUS]` 块驱动每 turn 的 breadcrumb（由 `inject-workflow-state.py` 解析）
- `### Skill Routing` 表把用户意图 → 自动触发的 skill

**用于**：自定义每 turn 的提醒、添加自定义 status（如 `[workflow-state:blocked]`）、重塑 phase、路由到项目特定的 skill。

**`trellis update` 安全**：整文件冲突处理。Trellis 不合并单个 `[workflow-state:*]` 块。对自定义 workflow，运行时冲突用 `trellis update --create-new`，marketplace workflow 预览用 `trellis workflow --template <id> --marketplace <source> --create-new`，然后 review 完整文件 diff。

定制后的 `workflow.md` 作为 `ai-native-harness-dev` marketplace workflow 分发。它的
executor 协议 breadcrumb、规划 review 门禁、AI Native Plan 路由、集成校验和
Codex inline 分支同时跨越状态块和 phase 段，因此部分块复制不是受支持的更新
策略。

### 4. 自定义 skill —— 自动触发的能力模块

一个 skill 是一个含 `SKILL.md` 的文件夹：

```
.opencode/skills/<name>/SKILL.md     # OpenCode
.claude/skills/<name>/SKILL.md       # Claude Code
.agents/skills/<name>/SKILL.md       # Codex/shared agentskills.io 根
.codex/skills/<name>/SKILL.md        # 仅 Codex 专用 skill
```

平台匹配的是 frontmatter 里的 `description`。要把它写成 **触发条件** 的描述，而不是 skill 的身份。

**用于**：可复用的 workflow 模块（brainstorm、before-dev、check、update-spec、break-loop 模式）。项目特定的能力（api-doc 生成、spec 脚手架、gate-result 记录）。

### 5. `.trellis/spec/` 团队规则 —— 工程指南

按 package 和 layer 组织的项目级约定：

```
.trellis/spec/
├── <package>/<layer>/index.md   # 入口，含 Pre-Development Checklist + Quality Check
└── guides/index.md              # 跨 package 的思考指南（本文件就在此）
```

通过 SessionStart hook + brainstorm/before-dev skill 注入到 AI context。

**用于**：编码规范、错误处理模式、测试规则、思考 checklist、契约引用。

**`trellis update` 安全**：团队拥有。模板更新不覆盖 package/layer spec。

## 决策树

```
Need Trellis to do something new?
│
├── Tied to task lifecycle (create/start/finish/archive)?
│   └── Use #1 (.trellis/config.yaml hooks)
│
├── Tied to AI tool calls or session events?
│   └── Use #2 (platform hooks)
│
├── Need to change what AI does each turn or how phases flow?
│   └── Use #3 (.trellis/workflow.md edit)
│
├── Need a reusable capability triggered by user intent?
│   └── Use #4 (custom skill)
│
├── Need to tell AI "follow this rule when writing code"?
│   └── Use #5 (.trellis/spec/)
│
└── None of the above?
    └── Re-read this guide. If still none, then maybe a fork is justified —
        but document the justification in decision-log.md first.
```

## 常见错误

### 错误：直接编辑 `.trellis/scripts/task.py`

**症状**：本地改动在下次 `trellis update` 时被覆盖；行为与 CLI 文档不一致。

**原因**：`.trellis/scripts/` 由 Trellis 管理。哈希记录在 `.trellis/.template-hashes.json`。

**修复**：若需对 task 生命周期事件作出反应，用 `.trellis/config.yaml` 的 `hooks`（#1）。若需要新的 CLI 形态，在项目 `scripts/` 里写一个 wrapper 脚本。

### 错误：替换 workflow.md 却不保留项目行为

**症状**：切换 workflow 后，AI 不再标证据等级、不再跑 self-review，或把复杂请求路由错。

**原因**：项目行为跨越 `[workflow-state:*]` 块、phase 步骤和路由表。整文件 workflow 替换会把它们覆盖。

**修复**：先用 `--create-new` 预览，把整个生成文件与活动中的
`.trellis/workflow.md` 比较。归属与更新契约见配套的 workflow bundle README。

### 错误：把运行时代码放进 `.trellis/spec/`

**症状**：spec 模板 marketplace 安装失败，或把 task 状态 / 平台 prompt 污染进目标项目。

**原因**：`.trellis/spec/` 仅用于工程指南。运行时脚本、task 状态、密钥和平台特定的 prompt 文件应放别处。

**修复**：让 spec 模板仅含指南。运行时 helper 位于
`.trellis/workflows/ai-native-harness-dev/scripts/`；见本 spec 模板
README 的"What this does NOT install"。

## 验证

在声称某个 extension 是必需的之前，先验证：

- [ ] 读 `trellis init --help` 与 `trellis workflow --help` 了解内建 flag
- [ ] 读 `.trellis/config.yaml` 看既有 hook
- [ ] 读 `.trellis/workflow.md` 看既有 breadcrumb 与 skill 路由
- [ ] 读 `.trellis/spec/guides/index.md` 看既有思考指南
- [ ] 检查该需求能否用项目 `scripts/` 里的 wrapper 脚本解决

只有 5 项全部穷尽后，才考虑 fork Trellis 源码。

## 参考

- `plan-artifact-contract.md` —— 文件状态归属
- `baseline-and-gate-result-protocol.md` —— evidence runtime v0 schema
- 配套的 workflow bundle README —— 安装矩阵与整文件更新契约
- Trellis 文档：`custom-hooks.mdx`、`custom-workflow.mdx`、`custom-skills.mdx`、`configuration.mdx`
