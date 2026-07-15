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
├── squad-briefing.md                ← 全队共识（流水线 + 角色 + 交接 + 门禁）
├── leader/
│   └── leader-prompt.md             ← 队长指令（调度逻辑）
├── skills/
│   ├── registry.json                ← 外部 skill 扒取清单（来源 + 锁定 + 新鲜度）
│   ├── planner/                     ← 规划员指令 + 同步来的 brainstorming / writing-plans
│   ├── implementer/                 ← 实现员指令 + 同步来的 tdd / executing-plans / ...
│   ├── reviewer/                    ← 审查员指令 + 同步来的 code-review / systematic-debugging
│   └── gate-runner/                 ← 门禁执行器指令 + 同步来的 verification-before-completion
├── pipeline/
│   ├── standard.yaml                ← 标准流水线定义
│   └── bugfix.yaml                  ← Bugfix 流水线定义
├── gates/                           ← 门禁脚本（11 个 Python 脚本）
│   ├── baseline.py                  ← 实现前后测试快照 + diff
│   ├── plan_contract_check.py       ← 规划制品完整性检查
│   ├── delivery_checklist.py        ← 交付完整性检查
│   ├── gate_result.py               ← 门禁事件记录
│   ├── rollback_counter.py          ← 回退计数 + 熔断
│   ├── scar_summary.py              ← 软门禁伤疤汇总
│   └── ...
├── guides/                          ← 方法论指南（11 篇）
│   ├── script-first-architecture.md ← 核心设计哲学
│   ├── executor-protocol.md         ← AI 行为约束
│   ├── evidence-and-quality-gates.md← 10 道质量门禁
│   └── ...
└── cli/
    ├── resume.py                    ← 快速唤起终端续聊（半成品）
    └── TASK.md                      ← CLI 开发任务书
```

## 快速开始

### 1. 在 Multica 上创建小队

在 [Multica](https://multica.ai) 上创建 5 个 agent：

| Agent | CLI | 职责 |
|---|---|---|
| 队长-Leader | Hermes / Claude | 调度编排 |
| 规划员-Planner | Claude / OpenCode | 写 prd/design/test-cases |
| 实现员-Implementer | Codex | 写代码 |
| 门禁执行器-GateRunner | 任意最省的 | 跑脚本 |
| 代码审查员-Reviewer | Claude / OpenCode | 读 diff 写 verdict |

把 `squad-briefing.md` 内容设为所有 agent 的共享上下文。把各自的 `prompt.md` 设为对应 agent 的 instructions。

### 2. 挂载 local_directory

```bash
multica project create my-project
multica project resource add my-project --type local_directory \
  --local-path /path/to/your/repo --daemon-id <daemon>
```

### 3. 发起一个需求

先拿到队长 agent 的 UUID，再用 `--assignee-id`（避免 fuzzy 中文名解析错路由）：

```bash
multica agent list --output json                                   # 取 队长-Leader 的 id
multica issue create --title "你的需求" --assignee-id <队长uuid> --status todo
```

队长会自动创建分阶段子 issue 并推进流水线。所有跨阶段状态写在 **parent issue 的 metadata**（原子并发安全 KV），各角色的准出裁定发在 **child issue 的评论**——见 `squad-briefing.md` 的「两层状态模型」。

## 方法论 skill（不自编，从社区同步 → 注册成 multica workspace skill）

角色 prompt（`skills/*/prompt.md`）只定义"你是谁"；具体方法论从社区优秀 repo 同步，**不自编**——对标应用宝 §2.2「skill 是注意力管理」。

三步流程：

```bash
# 1. 同步：从 obra/superpowers 等拉取 SKILL.md 到本地
python3 harness/cli/sync_skills.py sync

# 2. 注册：把本地 skill 注册成 multica workspace skill（幂等，按 name 查重）
python3 harness/cli/register_skills.py

# 3. 绑定：在 multica UI 把 skill bind 到对应 role agent（agent.skills）
#    register 会打印 role → skill_id 映射作为 bind 指引
```

**为什么走 multica 原生 skill 机制**：bind 后，task dispatch 时 multica daemon 把 SKILL.md 写进 workdir 的 provider 原生 skill 目录（`.claude/skills/`、`.pi/skills/`…），agent CLI 按 frontmatter `description` 自动触发（progressive disclosure）——所以 **role prompt 不引用 skill**，CLI 自己发现。映射是 **1 agent : N skill**（一个 skill 解决一类问题；Qoder 经验值 <10）。

**新鲜度**：用 multica autopilot（schedule，如每周）定时跑 `sync_skills.py check`，落后写 issue；确认后 `sync` + `register` 升级。每个 skill 带 `.source.json`（repo / commit / hash）。新增上游只需在 `skills/registry.json` 加一条 entry。

## 设计哲学

| 原则 | 来源 | 落地 |
|---|---|---|
| 脚本管确定性，Prompt 只留语义判断 | 腾讯 TEG | 11 个 Python 门禁脚本 |
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
