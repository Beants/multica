# Multica + Trellis：AI Agent 协作开发实践分享 — 30 分钟讲稿

> 面向同事的内部分享，语气谦虚务实，侧重真实使用体验、可复用方法和踩坑复盘。

## 分享目标

- 让同事快速理解 Multica、Trellis、SDD 各自解决什么问题
- 讲清楚我在实际使用里怎么把它们串起来
- 留下几条能直接借鉴到自己工作流里的经验
- 不做产品发布会，不追求讲全，只讲最有帮助的部分

---

## 第一部分：开场与背景（3 分钟）

大家好，我是马旭，刚加入数质中心。今天想跟大家分享一个我最近在用的 AI Agent 协作开发方案。

先说我的背景。之前我在一个小团队做开发，日常大量使用 AI coding 工具——Claude Code、Codex、Cursor Agent 这些。用下来发现一个痛点：这些工具本质上还是"一个人写 prompt，等结果"的单线程模式。如果你的项目有多个任务要并行推进，靠人手动一个一个去调度 AI，效率很快就会撞到天花板。

后来我发现了一个开源项目叫 Multica，它解决的核心问题就是：**让 AI agent 像团队成员一样被分配任务、自主执行、互相协作。** 不是一次只驱动一个 agent，而是可以同时管理多个 agent，让它们自己分工、协调、汇报。

今天分享的内容包括两部分：一是 Multica 和 Trellis 这套组合到底是什么、怎么工作的；二是我在实际使用中踩过的坑、积累的经验。Multica 不是我做的，是开源社区的项目，我是使用者。

---

## 第二部分：Multica 是什么（5 分钟）

### 2.1 架构概览

> 这部分只讲到“够理解工作方式”为止，不展开成架构课。

Multica 分三层：

```
┌──────────────┐     ┌──────────────┐     ┌──────────────────┐
│   Next.js    │────>│  Go Backend  │────>│   PostgreSQL     │
│   + Electron │<────│  (Chi + WS)  │<────│   (pgvector)     │
└──────────────┘     └──────┬───────┘     └──────────────────┘
                            │
                     ┌──────┴───────┐
                     │ Agent Daemon │  ← 跑在你的机器上
                     └──────────────┘
```

- **前端**：Next.js Web 端 + Electron 桌面端，共享 UI 组件
- **后端**：Go（Chi 路由 + gorilla/websocket 实时通信），PostgreSQL 17 + pgvector
- **Agent Daemon**：跑在你本地机器上，自动检测已安装的 AI CLI，把你的机器注册成一个 Runtime

### 2.2 工作原理：什么在云端，什么在本地

这一块很多人会困惑，我讲清楚一下。

**Multica Cloud（云端）：**
- Web 界面（看板、Issue 管理、Agent 配置、Settings）
- PostgreSQL 数据库（所有 issue、评论、agent 配置、workspace 数据）
- 任务调度器（接收 assign/mention 事件，派发任务给对应的 runtime）
- WebSocket 中继（实时把 agent 的执行进度推送给浏览器）
- 用户认证、workspace 隔离

**你的本地机器：**
- **Agent Daemon**（后台进程）——轮询云端领取任务，或者通过 WS 接收推送
- **AI CLI 工具**——Claude Code、Codex 等真正的执行者，跑在你本地
- **你的代码仓库**——agent 直接在你本地的 git repo 里工作，读写文件、创建分支、提交代码
- **API Key**——你的 AI 服务密钥留在本地，不经过 Multica Cloud

**数据流向：**

```
云端（Multica Cloud）              本地机器
┌─────────────────┐               ┌──────────────────────────┐
│ Web UI          │               │ Agent Daemon             │
│   ↕ WS          │◄──心跳/任务──►│   ↕ HTTP/WS              │
│ PostgreSQL      │  领取任务      │   │                      │
│   issue 数据    │◄─完成/评论────│   ├─► Claude Code CLI    │
│   agent 配置    │  推送进度      │   ├─► Codex CLI          │
│   调度逻辑      │               │   ├─► Cursor Agent CLI   │
└─────────────────┘               │   └─► ...（11种）        │
                                  │                          │
                                  │ 你的代码仓库 (git repo)   │
                                  │ API Keys (留在本地)       │
                                  └──────────────────────────┘
```

关键点：**你的代码不经过 Multica Cloud，你的 API Key 也不经过。** 云端只负责调度和状态管理，实际执行全在你本地。

### 2.3 Skills 和 MCP 与本地 CLI 的关系

这块容易混淆，我单独讲一下。

**原生的 AI CLI 本身就有能力：** Claude Code 能读写文件、跑命令、搜索代码。你直接用 Claude Code 写代码，它就是自己干活。

**Skills 是什么：** Skills 是给 Agent 加的"附加指令包"。一个 Skill 就是一个 SKILL.md 文件（加可选的配套文件），内容是给 agent 的工作指南。比如你可以写一个 Skill 叫 "code-review"，里面写"审查代码时关注这 5 个维度……"。Agent 执行任务时，Multica 会把这个 Skill 文件注入到 agent 的工作目录里，agent 读到后就按这个指南工作。

```
Multica Cloud                    本地
┌────────────┐                  ┌──────────────────────────┐
│ Skill 存储  │──任务领取时下发──►│ .agent_context/skills/   │
│ (数据库)    │                  │   ├─ code-review/SKILL.md│
└────────────┘                  │   └─ trellis-check/SKILL.md│
                                │                          │
                                │ AI CLI (如 Claude Code)   │
                                │   自动读取 skills/ 目录   │
                                │   按 Skill 指南执行任务    │
                                └──────────────────────────┘
```

**MCP（Model Context Protocol）服务是什么：** MCP 是给 AI CLI 添加外部工具的一种标准协议。比如你可以给 Claude Code 配一个 MCP Server，让它能查数据库、调 API、访问内部系统。Multica 支持给 Agent 配置 `mcp_config`（JSON 格式），执行任务时传给 AI CLI，这样 agent 就能用到你配置的 MCP 工具。

**总结一下三者关系：**

| | 本地 AI CLI | Skills | MCP 服务 |
|---|---|---|---|
| **是什么** | 执行者（Claude Code 等） | 附加指令包（SKILL.md） | 外部工具连接器 |
| **来源** | 你自己安装 | ClawHub / GitHub / 本地编写 | 你自己搭建或第三方 |
| **何时加载** | Daemon 调用 | 任务开始时注入到工作目录 | 任务开始时通过参数传入 |
| **类比** | 员工 | 工作手册 | 工具箱 |

### 2.4 核心工作流

1. 在 Web 界面创建一个 Issue（任务）
2. 把 Issue 分配给一个 Agent
3. Agent 通过本地 daemon 拿到任务，调用对应的 AI CLI 执行
4. 执行过程中通过 WebSocket 实时流式输出进度
5. Agent 在 issue 下评论报告进展，完成后更新状态

### 2.5 平台原生能力

**支持 11 种 Agent CLI：**
Claude Code、Codex、GitHub Copilot CLI、Cursor Agent、Gemini、Hermes、OpenClaw、OpenCode、Pi、Kimi、Kiro CLI

**任务生命周期：**
`空 → queued → dispatched → running → completed / failed / cancelled`

自动重试——runtime 掉线、超时会自动重新 dispatch。daemon 启动时还会自动回收上次异常中断的孤儿任务。

**Mention 触发系统：**
评论里用 `[@AgentName](mention://agent/<uuid>)` 触发对应 Agent。Agent 之间也可以互相 @mention，有防循环设计。

**技能系统（Skills）：**
从 ClawHub、skills.sh、GitHub 导入，或本地编写。执行时自动注入。

**Autopilot：**
定时（cron）、Webhook、API 触发。可以自动创建 issue + 分配。

**会话恢复：**
同一 agent 在同一 issue 上的连续任务自动恢复 session。

**实时 WebSocket：**
daemon WS 做心跳和任务推送，client WS 流式输出执行过程，500ms 批量推送。

### 2.6 Agent 的定位

**Agent 不是某个 AI 模型，而是一个角色。** 你创建一个叫 "Coder" 的 Agent，它背后可以用 Claude Code，也可以换成 Codex。模型可插拔，角色稳定。Agent 在 board 上跟人一样——有头像、有名字、可以被 assign、可以评论、可以创建 issue。

---

## 第三部分：方法论工具——Trellis 和 SDD（5 分钟）

### 为什么需要结构化方法论

为什么不直接给 Agent 一个 prompt 让它干？

核心问题：**AI agent 的上下文窗口有限，记忆不可靠。** 给它自然语言描述，它可能理解偏差。任务中断后恢复，它可能忘了之前在做什么。

所以在 Multica 之外，还有另一个开源项目 **Trellis**（github.com/mindfold-ai/Trellis，7800+ stars）——一个 AI coding agent 的 harness 框架。

### Trellis 是什么

解决三个问题：
1. **上下文管理**——agent 不用靠记忆，靠文件
2. **结构化流程**——Plan → Execute → Finish 三阶段
3. **知识沉淀**——任务完成后经验自动写回 spec

**目录结构：**
```
.trellis/
  spec/              # 编码标准和架构约束（版本控制）
  tasks/             # 每个任务一个目录
    MM-DD-<name>/
      prd.md           # Phase 1 产出
      implement.jsonl  # Phase 2 实现上下文
      check.jsonl      # Phase 2 审查上下文
      research/        # 调研产出
  workspace/         # 跨 session 持久记忆
```

**内置 Skills：**

| Skill | 作用 |
|-------|------|
| `trellis-brainstorm` | 交互式需求梳理，产出 prd.md |
| `trellis-implement` | 加载 implement.jsonl 引导实现 |
| `trellis-check` | 加载 check.jsonl 对照 spec 审查 |
| `trellis-research` | 调研子 agent，产出持久化 |
| `update-spec` | 经验写回 spec |
| `break-loop` | 检测并打断 agent 无限循环 |

### Multica + Trellis 结合

| | Multica | Trellis |
|---|---------|---------|
| **解决什么** | 多 agent 调度和协作 | 单 agent 的开发可靠性 |
| **类比** | 项目管理系统 | 开发方法论 |
| **核心技术** | mention 触发、任务生命周期、自动恢复 | spec 继承、JSONL 上下文注入、三阶段 |

- **Coder Agent** 用 `trellis-implement`——拿到注入了 spec + plan 的 implement.jsonl
- **Reviewer Agent** 用 `trellis-check`——拿到注入了验收标准的 check.jsonl
- **LongRunner Agent** 用 `trellis-research`——调研产出持久化到文件

### SDD 方法论

**Spec-Driven Development**，核心理念：

**人定义 WHAT，AI 实现 HOW。**

Spec 六要素：Problem Statement、Success Metrics（必须可量化）、User Stories、Acceptance Criteria、Non-Goals（防止 AI 加戏）、Constraints。

自检标准：**如果你的 Spec 换一个技术栈就不能用了，说明你在 Spec 里混入了实现细节。**

不是所有任务都走完整流程。三种入口：
- **类型 A**：复杂任务，写 Spec + Plan，走完整流水线
- **类型 B**：简单任务，直接 assign 给 Coder
- **类型 C**：不急的活，排给 LongRunner

---

## 第四部分：实际使用演示（10 分钟）

> 到这里大家已经了解了 Multica 的架构和 SDD 方法论，现在看一个完整的演示。

### 4.1 安装

```bash
brew install multica-ai/tap/multica    # macOS / Linux
multica setup                           # 一键：连接 Cloud + 登录 + 启动 daemon
```

自托管加 `--with-server`，用 Docker 部署完整后端。

### 4.2 确认 Runtime

`multica setup` 后 daemon 在后台运行，自动检测你装了哪些 AI CLI。Web 界面 Settings → Runtimes 可以看到你的机器和可用 provider。

### 4.3 创建 Agent

Settings → Agents → New Agent。选 Runtime + Provider + 起名。

### 4.4 实机演示：让 Agent 写一个"以 3 为基数"的 2048 游戏

> 这个 demo 只是为了演示从 Spec 到执行闭环，不代表业务目标。

我现在演示一个完整的任务流程——刚才讲了 SDD，现在就用 SDD 的方式来驱动。

**Step 1：本地写 Spec**

```markdown
# Spec: 三进制 2048

## Problem Statement
经典 2048 是以 2 为基数（2→4→8→16...），想要一个以 3 为基数的变体（3→9→27→81...），
在浏览器里能玩。

## Success Metrics
- 游戏可以正常运行，支持键盘方向键操作
- 合并逻辑正确：相同数字的方块碰撞时合并为 3 倍值（3+3=9, 9+9=27...）
- 初始随机生成两个 3
- 有分数显示
- 单个 HTML 文件，浏览器打开即玩

## User Stories
- 作为一个玩家，我想用方向键控制方块移动方向
- 作为一个玩家，我想看到每次合并后的分数变化

## Acceptance Criteria
- [ ] 方块可以上下左右移动
- [ ] 相同数字碰撞后合并为 3 倍值
- [ ] 无法移动时游戏结束并提示
- [ ] 显示当前分数

## Non-Goals
- 不做动画效果
- 不做移动端适配
- 不做排行榜

## Constraints
- 单个 HTML 文件，零依赖
```

**Step 2：本地写 Plan**

```markdown
# Plan: 三进制 2048

## 实现方案
1. 数据结构：4x4 二维数组，每个格子存储数值（0 表示空）
2. 渲染：用 HTML table + inline CSS，纯 JavaScript 控制
3. 合并逻辑：遍历每行/列，相邻相同数字合并为 3 倍
4. 输入：监听 keydown 事件（ArrowUp/Down/Left/Right）
5. 游戏结束检测：所有格子非空且无法合并
```

**Step 3：在 Multica 创建 issue，贴入 Spec + Plan，assign 给 Agent**

（实机操作演示）

**Step 4：观察 Agent 自主执行**

- Agent 领取任务
- 开始写代码（可以看到实时输出）
- 完成后评论汇报
- 我在浏览器打开生成的 HTML 文件验证

这个过程中我什么都不用做，就等着看结果。

### 4.5 多 Agent 协作——更复杂的场景

实际项目中，我配了四个 Agent 角色：

| 角色 | 职责 | 配置 |
|------|------|------|
| **Master Orchestrator** | 调度协调，不写代码 | 通过 Mention 触发，路由任务 |
| **Coder** | 写代码、跑测试 | 主力干活 |
| **Reviewer** | 代码审查、测试审查 | 跟 Coder 严格分离 |
| **LongRunner** | 长任务、中文文档 | 不急的活排队 |

流程：我写 Spec/Plan → assign 给 Master → Master 分配给 Coder → Coder 完成 @Master → Master 分配给 Reviewer → 审查通过 → Coder 创建 PR → 我 merge。

核心调度是 Agent 之间自动完成的，我主要介入写 Spec/Plan、review PR、merge。

**平台保障：**
- 防自审（Coder ≠ Reviewer 物理隔离）
- 防死循环（agent 回复不继承 mention）
- 自动重试（runtime 掉线恢复）
- 会话延续（自动恢复 session）
- 并发控制（max_concurrent_tasks）

---

## 第五部分：踩过的坑、提示词由来和怎么优化（5 分钟）

### 提示词是怎么来的

先交代一下背景——我给 Agent 写的那套提示词（最佳实践文档），不是凭空设计出来的。

我的做法是：把阿里、淘宝、美团等技术公众号上关于 AI Agent 协作开发的相关文章和文档收集起来，喂给模型，让它总结出一个基础版本的提示词。然后我在 Multica 上实际跑项目，根据运行中遇到的真实问题，一轮一轮地优化这个提示词。

所以接下来讲的每个坑，都是实际运行中遇到的问题，对应的规则也是为了解决这些具体问题加进去的。

### 坑 1：Agent 记忆不可靠

Agent 做完一个任务，下一个任务完全不记得之前做过什么。

**平台帮了什么**：同一 agent 在同一 issue 上自动恢复 session。

**平台原生能力 + 我自己补的约定**：Multica 原生能做的是同一 agent 在同一 issue 上自动恢复 session；Trellis 原生能做的是把 PRD、JSONL 上下文和 workspace journal 持久化到文件。`PROGRESS.md` 不是 Multica 或 Trellis 自带的，而是我自己补的一层约定——每次 run 结束必须更新进度文件，下一个 run 开始先读它。再加上一个"两步汇报"机制：Agent 每次完成任务必须 1）发一条结构化评论传上下文 2）assign 回 Master 保证触发。这样 Master 每次被唤醒时看评论就知道当前状态。

### 坑 2：Agent 自己审查自己

早期让同一个 Agent 写完代码就自己 review。结果就是——它永远觉得自己写的是对的。

**提示词里的规则**：**执行与评估分离。** 编码者不审查自己的代码，设计者不审查自己的方案。Coder 和 Reviewer 严格分成两个 Agent。

### 坑 3：Runtime 挂了

Agent 正在跑，突然 daemon 断了。

**平台帮了什么**：自动重试——runtime offline 后重新 dispatch，daemon 重启时回收孤儿任务。

**提示词里的规则**：Master Orchestrator 检测到 run 状态为 failed 且错误包含 "runtime went offline" 时，等 30 秒后重新 assign，最多重试 2 次，第 3 次升级到人。

### 坑 4：迭代失控

Reviewer 打回 → Coder 修 → 再打回 → 再修……循环五次还在扯。

**提示词里的规则**：代码/测试评审最多 2 轮。超过就升级到人来看。这条是从实践中提炼的硬性上限。

### 坑 5：429 限速

模型 API 限速，Agent 被拒绝后直接失败。

**提示词里的规则**：遇到 429 错误等待 60 秒后重试，最多 5 次。5 次失败后汇报给 Master 请求指示。

### 怎么优化你自己的提示词

我分享的这套提示词是基于我个人实践打磨的，不一定适合所有人的场景。不同的团队、不同的项目、不同的工作流，提示词应该不一样。

那怎么优化呢？有一个比较实用的方法——**用 AI 来优化 AI 的提示词**。

具体做法：

1. **Multica 支持 CLI 操作**——你可以用 `multica issue comment add`、`multica issue assign` 这些命令在本地操作
2. **Claude Code 本身就能读写文件**——你可以直接在本地用 Claude Code 编辑 Agent 的提示词
3. **把运行日志喂给 Claude Code**——当 Agent 执行出错或者表现不好的时候，把 issue 评论历史、执行日志复制出来，让 Claude Code 分析问题、给出提示词修改建议
4. **迭代优化**——改完提示词后重新跑任务，看效果是否改善，不满意再继续调

```
Agent 执行出错
    ↓
复制 issue 评论 + 执行日志
    ↓
用 Claude Code 分析：提示词哪里导致了这个问题？
    ↓
Claude Code 给出修改建议并直接编辑提示词
    ↓
重新跑任务验证
    ↓
效果 OK → 提交；效果不好 → 继续迭代
```

本质上就是：**Multica 提供了 CLI 接口，Claude Code 提供了分析和编辑能力，两者组合就能形成一个提示词优化的闭环。** 你不需要手动琢磨提示词怎么写——跑起来看哪里不对，让 AI 帮你改就行。

### 诚实的总结

这套东西确实能放大个人能力，但不是银弹：

1. 提示词需要根据实际场景持续调优——我那个"最佳实践"文档是跑了大量任务后迭代出来的
2. 前期调试成本不低——光是让 Agent 稳定跑起来就花了不少时间
3. Spec 和 Plan 还得人写——这部分目前 AI 做得还不够好
4. 对 Agent 的产出保持审查习惯——它不是万能的
5. **好消息是提示词优化本身可以用 AI 来做**——不需要你一个人死磕

---

## 第六部分：总结与 Q&A（2 分钟）

- **Multica**：开源 AI Agent 协作平台，支持 11 种 CLI，有完整任务生命周期、mention 触发、Skills、自动恢复。你的代码和 API Key 都留在本地，云端只管调度
- **Trellis**：开源 AI agent harness，通过 spec + JSONL 注入 + 三阶段工作流让 AI 编码更可靠
- 两者组合：Multica 管"多 agent 怎么协作"，Trellis 管"单个 agent 怎么干得靠谱"
- 提示词是实践出来的——参考业界文档定基线，跑真实任务持续优化
- **提示词优化也可以用 AI**——Multica CLI + Claude Code 形成优化闭环
- 角色分离（Coder ≠ Reviewer）+ 结构化输入 + 迭代上限是保证质量的关键

项目地址：
- Multica：github.com/multica-ai/multica
- Trellis：github.com/mindfold-ai/Trellis

有问题可以随时问。

---

## 时间分配

| 部分 | 时长 | 要点 |
|------|------|------|
| 开场与背景 | 3 min | 加入数质中心、个人背景、发现 Multica |
| Multica 是什么 | 5 min | 架构、云端vs本地、Skills/MCP 与 CLI 关系、原生能力 |
| **Trellis 和 SDD** | **5 min** | **Trellis 是什么、两者结合、SDD 方法论、Spec 六要素** |
| **实际使用演示** | **10 min** | **三进制 2048（SDD 驱动）、多 Agent 协作** |
| 踩坑、提示词由来与优化 | 5 min | 提示词来源、5 个坑、怎么用 AI 优化提示词 |
| 总结 + Q&A | 2 min | 核心价值回顾 |

**总计：30 分钟**
