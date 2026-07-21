# Harness Squad — 基于 Multica 的 AI 原生开发流水线

> 让 AI 的行为可控、证据可审计、质量可证明。

## 这是什么

一套可以装在 Multica 上的 **门禁 + 证据 + 小队编排** 层。不造平台、不造 agent、不造 CLI——只做"胶水层"：定义流水线阶段、挂门禁脚本、编排小队角色。

**核心主张**：脚本管确定性，Prompt 只留语义判断。

## 目录结构

```
harness/
├── README.md                        ← 你在这里
├── docs/                            ← 设计文档 + 参考资料 + 实验报告
│   ├── design.md                    ← 完整设计文档（架构 + 用户故事）
│   ├── bulk-experiment-report.md    ← 30 次对比实验（零方差数据）
│   ├── ref-*.md                     ← 行业参考（腾讯/TAB/SwarmAI/Multica）
│   └── ref-multica-*.md|.go         ← Multica 源码文档
├── squad-briefing.md                ← squad instructions（全队共识：触发机制 + 流水线 + 角色 + 交接 + 门禁）
├── leader/
│   └── leader-prompt.md             ← 队长 agent instructions（调度逻辑）
├── skills/
│   ├── registry.json                ← 外部 skill 扒取清单（来源 + 锁定 + 新鲜度）
│   ├── planner/                     ← 规划员 agent instructions + 同步来的 brainstorming / writing-plans
│   ├── implementer/                 ← 实现员 agent instructions + 同步来的 tdd / executing-plans / ...
│   ├── reviewer/                    ← 审查员 agent instructions + 同步来的 code-review / systematic-debugging
│   └── gate-runner/                 ← 门禁执行器 agent instructions + harness-gates skill (含 gates/*.py) + verification-before-completion
├── pipeline/
│   ├── standard.yaml                ← 标准流水线定义
│   └── bugfix.yaml                  ← Bugfix 流水线定义
├── guides/                          ← 方法论指南（11 篇）
│   ├── script-first-architecture.md ← 核心设计哲学
│   ├── executor-protocol.md         ← AI 行为约束
│   ├── evidence-and-quality-gates.md← 10 道质量门禁
│   └── ...
└── cli/
    ├── sync_skills.py               ← 从社区 repo 同步 skill（sync / check）
    ├── register_skills.py           ← 注册 skill 到 multica + bind role agent
    └── resume.py                    ← 快速唤起终端续聊（半成品）
    └── TASK.md                      ← CLI 开发任务书
```

## 快速开始

### 1. 在 Multica 上创建小队

在 [Multica](https://multica.ai) 上创建 1 个 squad + 5 个 agent：\n
| Agent | CLI | 职责 |
|---|---|---|
| 队长-Leader | Hermes / Claude | 调度编排 |
| 规划员-Planner | Claude / OpenCode | 写 prd/design/test-cases |
| 实现员-Implementer | Codex | 写代码 |
| 门禁执行器-GateRunner | 任意最省的 | 跑脚本 |
| 代码审查员-Reviewer | Claude / OpenCode | 读 diff 写 verdict |

**Instructions 分离**（关键）：

- **Squad instructions** ← `squad-briefing.md`（全队共识：平台触发机制、流水线、角色、交接、门禁）
- **Agent instructions** ← 各自 `prompt.md`（角色专属行为，不含全队共识）

Multica 平台会在 task dispatch 时分别注入 squad instructions 和 agent instructions，不要把全队共识复制进每个 agent。

**门禁脚本**：打包成 `harness-gates` skill（`skills/gate-runner/harness-gates/` 下含 SKILL.md + `gates/` 13 个脚本），绑到门禁执行器 agent。task dispatch 时 daemon 自动把脚本写进 workdir 的 `.claude/skills/harness-gates/gates/`，不需要项目 repo 自带。门禁执行器 prompt 里用 `GATES_DIR` 变量动态定位。

### 2. 挂载 local_directory

```bash
multica project create my-project
multica project resource add my-project --type local_directory \
  --local-path /path/to/your/repo --daemon-id <daemon>
```

> 门禁脚本不需要在项目 repo 里。`harness-gates` skill 绑在门禁执行器 agent 上，每次 task dispatch 时 daemon 自动注入 workdir。

### 3. 发起一个需求

先拿到队长 agent 的 UUID，再用 `--assignee-id`（避免 fuzzy 中文名解析错路由）：

```bash
multica agent list --output json                                   # 取 队长-Leader 的 id
multica issue create --title "你的需求" --assignee-id <队长uuid> --status todo
```

队长会自动创建分阶段子 issue 并推进流水线。所有跨阶段状态写在 **parent issue 的 metadata**（原子并发安全 KV），各角色的准出裁定发在 **child issue 的评论**——见 `squad-briefing.md` 的「两层状态模型」。

## Skill 体系

Harness 有两类 skill，都通过 `register_skills.py` 注册到 Multica workspace skill，再 bind 到对应 role agent：

### 类一：社区方法论 skill（同步）

从 [obra/superpowers](https://github.com/obra/superpowers) 同步，不自编——对标应用宝「skill 是注意力管理」。角色 prompt（`skills/*/prompt.md`）只定义"你是谁"；具体方法论从社区优秀 repo 同步。

```bash
# 1. 同步：从上游 repo 拉取 SKILL.md 到本地
python3 harness/cli/sync_skills.py sync

# 2. 注册：把本地 skill 注册成 multica workspace skill（幂等，按 name 查重）
python3 harness/cli/register_skills.py

# 3. 绑定：register 会自动 bind 到匹配的 role agent
```

**新鲜度**：用 multica autopilot 定时跑 `sync_skills.py check`，落后写 issue。每个 skill 带 `.source.json`（repo / commit / hash）。

### 类二：自造工程 skill（harness-gates）

`harness-gates` 是自造 skill（`repo: null` in registry.json），包含 13 个门禁脚本。不走社区同步，直接由 `register_skills.py` 注册。

```
skills/gate-runner/harness-gates/
├── SKILL.md           ← 告诉 agent 脚本在哪、怎么调用
└── gates/             ← 13 个 Python 门禁脚本
    ├── plan_contract_check.py
    ├── baseline.py
    ├── api_gate.py
    └── ...
```

register_skills.py 对两类 skill 统一处理：`repo != null` 走社区同步路径，`repo == null` 走本地自造路径。注册时都会 upsert SKILL.md + 所有 files 到 Multica。

### Skill 绑定关系

| Agent | 社区 skill | 自造 skill |
|---|---|---|
| 规划员-Planner | brainstorming, writing-plans | — |
| 实现员-Implementer | executing-plans, test-driven-development, receiving-code-review | — |
| 门禁执行器-GateRunner | verification-before-completion | **harness-gates** |
| 代码审查员-Reviewer | requesting-code-review, systematic-debugging | — |
| 队长-Leader | — | — |

**为什么走 multica 原生 skill 机制**：bind 后，task dispatch 时 multica daemon 把 SKILL.md + files 写进 workdir 的 provider 原生 skill 目录（`.claude/skills/`、`.pi/skills/`…），agent CLI 按 frontmatter `description` 自动触发（progressive disclosure）。映射是 **1 agent : N skill**。新增上游只需在 `skills/registry.json` 加一条 entry。

## 设计哲学

| 原则 | 来源 | 落地 |
|---|---|---|
| 脚本管确定性，Prompt 只留语义判断 | 腾讯 TEG | 13 个 Python 门禁脚本 |
| Agent completed ≠ Business completed | 腾讯"凌晨三点" | 人工验收独立阶段 |
| 下游不可修改上游产物 | 腾讯 TAB | squad-briefing 铁律 |
| 能判定的就别留在 Rule 里 | 腾讯 TAB | 三层门禁硬度（硬/半硬/软） |
| 写测试的人不执行测试 | 腾讯应用宝 | 测试用例两段式 |
| 确定性编排替代概率性发挥 | 腾讯 TAB | Multica stage 屏障硬调度 |
| 知识必须能死 | SwarmAI | spec_freshness.py |

## 实验数据

30 次对比实验（同一需求 × 三组环境 × 各 10 次）：

| 指标 | 裸 Claude Code | Trellis 原生 | 本 Harness |
|---|---|---|---|
| PRD 产出率 | **0/10** | 8/10 | **10/10** |
| 基线证据 | **0** | **0** | **26 条事件** |
| 用户控制点 | 0 | 1 | 4 |

详见 `docs/bulk-experiment-report.md`。

## License

MIT
