# 贡献指南 — Harness 层

> Harness 是装在 Multica 之上的**胶水层**：定义流水线阶段、挂门禁脚本、编排小队
> 角色。它不造平台、不造 agent、不造 CLI——靠调用 Multica 的 CLI、读写 Multica 的
> 数据结构、注入 Multica workdir 来工作。
>
> 因此每一条 harness 改动，都必须对着 Multica 的**真实代码**核对，不能凭文档/记忆
> 拍脑袋。本指南把「拉 multica 代码 → 梳理相关逻辑 → 核对 MR 是否合理」固化为可
> 机械执行的步骤。

---

## 0. 为什么必须先拉 multica 的代码

Harness 的每一类产物都直接依赖 Multica 的运行时契约：

| Harness 产物 | 依赖的 Multica 契约 | Multica 源码位置 |
|---|---|---|
| `cli/register_skills.py` | `multica skill create/list` 命令、frontmatter `name`/`description` 解析 | `server/cmd/multica/cmd_skill.go`、`server/internal/handler/skill_create.go`、`server/internal/skill/frontmatter.go` |
| `cli/sync_agents.py` | `multica agent`/`squad` 命令、name 模糊匹配、instructions 分离注入 | `server/cmd/multica/cmd_agent.go`、`cmd_squad.go` |
| `cli/sync_skills.py` | multica workspace skill 注册路径、`.source.json` 锁定范式 | `server/internal/handler/skill.go`、根 `skills-lock.json` |
| `skills/gate-runner/harness-gates/gates/*.py` | `multica issue get`、issue metadata KV、parent/child issue、`.harness/<parent-issue-id>/` 隔离 | `server/cmd/multica/cmd_issue.go`、`cmd_issue_metadata.go`、`server/internal/handler/issue.go`、`issue_metadata.go`、`issue_child_done.go`、`issue_trigger.go` |
| `pipeline/*.yaml` | Multica stage 屏障、workflow 引擎 | `server/internal/workflow/`、`server/cmd/multica/cmd_workflow.go` |

**如果 multica 上游改了上面任何一项（命令重命名、字段重命名、metadata 结构变
化、frontmatter 解析规则变化），你的 harness MR 可能跑不通，甚至静默写坏
数据。** 所以贡献前必须先核对真实代码。

> 已归档的参考快照在 `harness/docs/ref-multica-*.md`（如 issue #1943、PR #1281、
> CLI/daemon 指南）。但**它们是快照，不是事实来源**——上游可能已经变化。MR 审查
> 以 `upstream` 最新代码为准。

---

## 1. 贡献前必做：拉取 multica 代码

### 1.1 确认你的 fork 关联了 upstream

本仓库（`harness/` 所在 repo）已配置：

```
origin    https://github.com/Beants/multica.git         (你的 fork)
upstream  https://github.com/multica-ai/multica.git     (Multica 官方)
```

若你 clone 的是别的 fork，先补 upstream：

```bash
git remote add upstream https://github.com/multica-ai/multica.git
```

### 1.2 同步上游 main，拿到最新 multica 源码

```bash
git fetch upstream
git checkout main
git merge --ff-only upstream/main        # 或 git rebase upstream/main
```

### 1.3（可选但推荐）单独 clone 一份纯 upstream 做参照

如果你在 fork 上有未提交改动、不想被干扰：

```bash
git clone https://github.com/multica-ai/multica.git /tmp/multica-ref
cd /tmp/multica-ref
git log --oneline -1                      # 记下你核对所基于的 commit
```

**核对清单（机械可验证）**：

- [ ] `git remote -v` 能看到 `upstream → multica-ai/multica`
- [ ] `git log upstream/main --oneline -1` 拿到了**提交时间在本次 MR 创建前**的 commit
- [ ] 在 MR 描述里记下你核对所基于的 upstream commit SHA（写进「核对基线」一栏）

---

## 2. 梳理 multica 的相关代码逻辑

针对你的改动会触碰的 harness 模块，去 multica 源码里把对应逻辑读一遍，确认
harness 的假设和 multica 的实现一致。下面给出每个 harness 模块的「对照阅读
清单」。**不要跳过——这是本指南的核心要求。**

### 2.1 改 `cli/`（注册/同步脚本）

这些脚本 shell out 调 `multica` CLI。核对每一处 `multica <subcommand>` 调用：

```bash
# 例如 register_skills.py 里调了 multica skill create / list / file add
grep -rn 'run_mc\|subprocess.*multica\|"multica"' harness/cli/*.py
```

然后逐条去 multica 源码确认命令名、子命令、flag、输出 JSON 字段没变：

| CLI 子命令 | multica 源码 |
|---|---|
| `multica skill ...` | `server/cmd/multica/cmd_skill.go` |
| `multica agent ...` | `server/cmd/multica/cmd_agent.go` |
| `multica squad ...` | `server/cmd/multica/cmd_squad.go` |
| `multica issue ...` | `server/cmd/multica/cmd_issue.go` |
| `multica issue metadata ...` | `server/cmd/multica/cmd_issue_metadata.go` |
| `multica workspace ...` | `server/cmd/multica/cmd_workspace.go` |
| `multica project ...` | `server/cmd/multica/cmd_project.go` |
| `multica workflow ...` | `server/cmd/multica/cmd_workflow.go` |

**必须确认**：

- [ ] 命令名/子命令名拼写与 multica 一致（曾出现 `multica skill` vs `multica skills` 的混淆）
- [ ] `--output json` 的字段路径（`data[].id` / `data[].name` …）与 multica 序列化一致
- [ ] frontmatter 解析规则（`name`/`description`）与 `server/internal/skill/frontmatter.go` 一致
- [ ] 幂等判定键（按 `name` 查重）与 multica 唯一约束一致

### 2.2 改 `skills/gate-runner/harness-gates/gates/*.py`（门禁脚本）

门禁脚本读 `multica issue get` 的输出、读写 `.harness/<parent-issue-id>/` 下的
artifact、依赖 `MULTICA_ISSUE_ID` / `HARNESS_PARENT_ISSUE_ID` 环境变量。核对：

- [ ] **parent/child issue 关系**：`server/internal/handler/issue_child_done.go` —— 确认 child issue 完成 → parent 推进的语义没变
- [ ] **issue metadata KV**：`server/cmd/multica/cmd_issue_metadata.go` + `server/internal/handler/issue_metadata.go` —— 确认 metadata 是原子 KV、key/value 约束没变（harness 把跨阶段状态写在这里）
- [ ] **issue 触发**：`server/internal/handler/issue_trigger.go` —— 确认 task dispatch 触发链没变
- [ ] **workdir 注入**：`server/internal/handler/runtime_local_skills.go` + `server/internal/daemon/local_skills.go` —— 确认 SKILL.md + files 写进 workdir 的路径（`.claude/skills/`、`.pi/skills/`）没变
- [ ] `.harness/<parent-issue-id>/` 的目录约定与 `task_resolver.py` 的 `HARNESS_DIRNAME` / `resolve_task_dir` 一致

### 2.3 改 `pipeline/*.yaml`（流水线定义）

流水线依赖 Multica 的 stage 屏障 / workflow 引擎做硬调度：

- [ ] `server/internal/workflow/` —— 确认 stage 屏障、状态流转语义没变
- [ ] `server/cmd/multica/cmd_workflow.go` —— 确认 workflow import 字段没变

### 2.4 改 squad / agent instructions（`squad-briefing.md`、`skills/*/prompt.md`）

- [ ] 确认 instructions 分离注入：squad instructions vs agent instructions（见
      `sync_agents.py` 顶部说明），不要把全队共识复制进每个 agent prompt
- [ ] 确认 role→agent name 模糊匹配规则（`leader`/`planner`/`implementer`/
      `reviewer`/`gate-runner`）仍是 `sync_agents.py` 依赖的约定

### 2.5 改 `skills/registry.json` 或新增外部 skill

- [ ] 对照 multica 根目录 `skills-lock.json` 的锁定范式
- [ ] `.source.json` 的 `repo`/`ref`/`pinned`/`hash` 字段含义与 `sync_skills.py` 一致
- [ ] `repo: null`（自造 skill）走本地路径，`repo != null` 走社区同步——确认你的 entry 类型正确

---

## 3. 核对 MR 是否合理

在提 MR 前，用下面的核对清单逐条验证。**每一条都要能答出「是 / 不适用」，答
不出就回去补。**

### 3.1 契约一致性（基于第 2 步的梳理）

- [ ] 改动触碰的每一个 `multica <cmd>` 调用，都在上游源码里找到了对应实现，且命令名/flag/输出字段一致
- [ ] 改动依赖的 issue metadata / parent-child / workdir 注入等契约，与上游当前实现一致
- [ ] 若 multica 上游有**未合并**的相关 PR/issue（参考 `harness/docs/ref-multica-pr-*.md` 的归档），MR 描述里说明是否依赖它、以及上游未合并的风险

### 3.2 行为可验证

- [ ] 新增/改动的脚本有可复现的验证步骤（命令 + 期望输出），写在 MR 描述里
- [ ] 涉及门禁脚本的，至少跑过一次 `python3 harness/skills/gate-runner/harness-gates/gates/<script>.py` 在真实 workdir 上
- [ ] 涉及 CLI 脚本的，至少跑过一次 `--dry-run` + 一次真跑（`multica login` 已就绪）

### 3.3 不破坏既有约定

- [ ] 没有把全队共识复制进单个 agent prompt（违反 instructions 分离）
- [ ] 没有让下游角色修改上游产物（违反 squad-briefing 铁律）
- [ ] 没有引入未审计的第三方依赖（harness CLI 脚本坚持标准库零依赖）
- [ ] 没有把确定性判定塞回 Prompt（应写进门禁脚本）

### 3.4 文档与参考同步

- [ ] 若改动涉及 multica 行为的新认知，更新 `harness/docs/ref-multica-*.md` 或新增参考文档（带原文链接 + 归档日期）
- [ ] 若改动涉及方法论/设计哲学，同步 `harness/guides/` 相应指南
- [ ] MR 描述包含「核对基线」：所基于的 upstream commit SHA + 核对时间

---

## 4. MR 描述模板

提 MR 时，描述里至少包含以下几节：

```markdown
## 改动摘要
<一两句话说清楚改了什么 harness 模块、为什么>

## 核对基线
- upstream commit: <SHA>  (<提交时间>)
- 核对时间: YYYY-MM-DD

## 对照梳理（参考 harness/CONTRIBUTING.md 第 2 节）
- 触碰模块: cli/ register_skills.py
- 对照 multica 源码: server/cmd/multica/cmd_skill.go, server/internal/skill/frontmatter.go
- 一致性结论: <命令名/字段/解析规则是否一致；如不一致说明应对>

## 验证步骤
1. <可复现命令>
2. <期望输出>

## 依赖的上游 PR/issue（若有）
- multica-ai/multica#<编号> — 状态: <Open/Merged> — 依赖原因: <>
```

---

## 5. 不可接受项（直接打回）

- ❌ 未拉取 / 未核对 multica 上游代码就提 MR
- ❌ MR 描述里没有「核对基线」（upstream commit SHA）
- ❌ 调了 `multica <cmd>` 但没在 multica 源码里确认该命令存在
- ❌ 改动依赖 multica 某 PR，但该 PR 未合并且 MR 未声明风险
- ❌ 把全队共识复制进 agent prompt / 让下游改上游产物

---

## 6. 相关文档

- [Harness README](./README.md) — 整体定位、快速开始、设计哲学
- [Guides 索引](./guides/index.md) — 11 篇方法论指南（executor 协议、证据 schema、门禁协议…）
- [Squad Briefing](./squad-briefing.md) — 全队共识：触发机制、流水线、角色、交接、门禁
- [Multica 参考快照](./docs/) — `ref-multica-*.md`（CLI/daemon 指南、issue #1943、PR #1281）
- Multica 产品自身的开发流程见仓库根目录 [`CONTRIBUTING.md`](../CONTRIBUTING.md)（那是开发 multica 本体用的，和本文件正交）
