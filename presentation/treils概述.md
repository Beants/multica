概述
报告问题
13 分钟
等级: 入门
Trellis 是一款团队级 AI 编码套件，它将你通常塞入 CLAUDE.md、AGENTS.md 或 .cursorrules 中的单体系统提示词，转化为一个渐进式、按需加载的规范、任务、工作流和日志知识库。无需再将整个项目上下文硬塞进每一次提示词中，Trellis 为每个 AI 编码 Agent —— Claude Code、Cursor、Codex、OpenCode、Pi Agent 等 —— 提供相同的项目事实来源，且仅在需要时才加载。

Trellis Demo

来源: README.md

Trellis 存在的意义
AI 编码 Agent 功能强大，但在团队规模下却难以可靠运作。根本问题不在于能力，而在于上下文。缺乏结构化约束，Agent 会在不同会话间遗忘决策、无视团队规范，且毫无边界地操作。传统的应对方式是把所有内容堆砌到单一指令文件中，但这种方式会因自身臃肿而崩溃：文件演变为庞然大物，上下文窗口被塞满，且根本无法维护。

Trellis 采用了一种不同的方式：渐进式、作用域受限的加载。团队标准存放于规范（Spec）中，任务决策存放于 PRD 中，会话记忆存放于日志（Journal）中。Agent 仅加载与当前工作阶段相关的上下文——不多不少，恰到好处。

来源: README.md, CLAUDE.md

四层架构
Trellis 会在你的仓库中安装一个 .trellis/ 目录，该目录围绕四个独立的知识层进行组织。每一层各司其职，共同构成一个 Agent 按需读取的完整上下文系统。

层级	路径	用途	编写者
规范 (Spec)	.trellis/spec/	按包和层级组织的团队编码标准与指南。Agent 在编写代码前会自动加载相关规范。	人类 + Agent（通过 update-spec）
任务 (Tasks)	.trellis/tasks/	PRD、任务上下文、状态、评审记录与验收标准。每个任务都是一个独立的目录，包含其专属的 prd.md、implement.jsonl 和 check.jsonl。	Agent（头脑风暴） + 人类
工作区 (Workspace)	.trellis/workspace/	开发者专属日志、决策与交接备忘，用于跨会话的连续性。日志自动限制为 2,000 行。	Agent（自动记录）
工作流 (Workflow)	.trellis/workflow.md	共享的开发生命周期：计划 → 执行 → 完成。包含每次轮次的面包屑状态，由钩子注入到每个提示词中。	Trellis（托管）
来源: README.md, workflow.md, paths.ts

核心循环
Trellis 工作流遵循一个严谨的循环，可有效防止 AI 辅助编码常见的失败模式——范围失控、决策遗忘与重复犯错。








该循环强制确立了三个关键属性：任务优先思维（编码前先捕获）、边界化实现（Agent 在注入规范的限定范围内工作），以及知识提升（经验回流至规范中，避免重蹈覆辙）。

来源: README.md, workflow.md

三阶段工作流
Trellis 中的每个任务都会经历三个阶段，每个阶段都有明确的进入和退出标准。这不是抽象的指导原则——它通过工作流状态面包屑强制执行，钩子会将这些状态注入到每一次 AI 交互中。

阶段	步骤	关键动作	状态
1 — 计划	1.0–1.5	创建任务 → 头脑风暴 PRD → 研究 → 配置上下文 (jsonl) → 激活	planning
2 — 执行	2.1–2.3	根据加载的规范实现 → 质量检查 → 必要时回滚	in_progress
3 — 完成	3.1–3.4	评审 → 用经验更新规范 → 提交 → 归档	→ completed
工作流状态面包屑是决定 AI 在每次交互中应执行操作的唯一事实来源。它被嵌入在 workflow.md 中，并在每次对话开始时由平台钩子读取——脚本中没有内置任何后备字典。

来源: workflow.md

平台适配器系统
Trellis 不绑定于任何单一的 AI 编码工具。它采用配置器模式——每个受支持的平台都有一个专用配置器，能够根据该平台的约定生成正确的入口点（命令、钩子、技能、Agent、设置）。所有平台上的 .trellis/ 中的核心知识保持完全一致。














AI_TOOLS 中的平台注册表定义了每个平台的能力——包括是否支持钩子、子 Agent、.agents/skills/ 标准，以及命令的引用方式。添加新平台只需三个步骤：定义数据条目、编写配置器，以及创建模板目录。

平台	配置目录	钩子	子 Agent	命令风格
Claude Code	.claude/	✅	✅	/trellis:name
Cursor	.cursor/	✅	✅	/trellis-name
Codex	.codex/ + .agents/skills/	❌	✅	$ name
Gemini CLI	.gemini/ + .agents/skills/	✅	✅	/trellis:name
GitHub Copilot	.github/copilot/	✅	✅	/ name
OpenCode	.opencode/	❌	✅	/trellis:name
Kiro Code	.kiro/skills/	✅	✅	$ name
Pi Agent	.pi/	✅	✅	/trellis-name
来源: ai-tools.ts, configurators/index.ts

技能系统
Trellis 0.5 是技能优先的。技能是结构化的提示词，用于在特定的工作流节点引导 AI 的行为。它们是工作流状态机与实际代码生成之间的智能层。

技能	触发条件	用途
brainstorm	新任务 / 需求不明确	协作式 PRD 探索——逐一提问，研究优先
before-dev	即将编写代码	加载规范，验证上下文，准备实现边界
check	编码完成	质量关卡：代码检查、类型检查、测试、规范合规性、跨层级审查
break-loop	陷入停滞 / 重复犯错	逃离循环推理的元技能
update-spec	获得值得捕获的经验	将任务中的发现提升为永久规范
在支持钩子的平台上（Claude Code、Cursor、Gemini CLI），会话上下文会通过 session-start.py 钩子自动加载。在不支持钩子的平台上，请使用 /start 或 /trellis:start 手动加载等效上下文。

来源: start.md, brainstorm.md, check.md

Trellis 生成的文件
运行 trellis init -u your-name 会搭建完整的项目结构。以下是最终将出现在你仓库中的内容：

your-project/
├── AGENTS.md                          ├── .trellis/
│   ├── workflow.md                    # 三阶段生命周期 + 面包屑状态
│   ├── config.yaml                    # 项目配置
│   ├── .developer                     # 开发者身份（已 gitignore）
│   ├── .current-task                  # 活跃任务指针（已 gitignore）
│   ├── spec/                          # 按包/层级的团队标准
│   │   ├── guides/index.md           # 跨包思考指南
│   │   ├── backend/                   # 后端指南（来自模板）
│   │   └── frontend/                  # 前端指南（来自模板）
│   ├── tasks/                         # 活跃和已归档的任务
│   │   └── MM-DD-name/               # 每个任务都是一个独立目录
│   │       ├── prd.md                # 产品需求
│   │       ├── task.json             # 元数据 + 状态
│   │       ├── implement.jsonl       # 实现 Agent 的规范上下文
│   │       └── check.jsonl           # 审查 Agent 的规范上下文
│   ├── workspace/your-name/          # 开发者日志 + 会话记录
│   │   ├── index.md                  # 个人会话索引
│   │   └── journal-1.md              # 会话日志（自动限制为 2000 行）
│   └── scripts/                      # Python 自动化层
│       ├── task.py                   # 任务生命周期 CLI
│       ├── get_context.py            # Agent 上下文注入
│       ├── add_session.py            # 日志条目写入器
│       ├── init_developer.py         # 开发者身份设置
│       └── common/                   # 共享脚本模块
├── .claude/                           # 为 Claude Code 生成
│   ├── commands/trellis/             # 斜杠命令（start、continue、finish-work）
│   ├── agents/                       # 子 Agent 定义
│   ├── skills/                       # 技能提示词
│   ├── hooks/                        # SessionStart + PreToolUse 钩子
│   └── settings.json                 # 平台设置
├── .cursor/                           # 为 Cursor 生成
│   └── ...                           # 相同模式，遵循 Cursor 约定
└── .codex/                            # 为 Codex 生成
    └── ...                           # + 写入共享的 .agents/skills/
特定平台的目录（.claude/、.cursor/ 等）由 Trellis 生成和管理。切勿手动编辑它们——你的修改将在 trellis update 时被覆盖。所有自定义内容都应放在 .trellis/ 中。

来源: paths.ts, markdown/index.ts, init.ts

快速参考：常用命令
命令	使用时机	作用
trellis init -u name	首次使用	搭建 .trellis/ + 生成平台适配器
trellis update	Trellis 升级后	刷新生成的文件，运行迁移
/start 或 /trellis:start	新会话（无钩子平台）	加载上下文：身份、任务、规范、工作流
task.py create "title"	开始新工作	创建任务目录 + 设置活跃任务
task.py start <name>	PRD 就绪时	将状态切换为 in_progress，开始阶段 2
/trellis:continue	任务进行中	推进当前任务至下一个工作流步骤
/trellis:finish-work	提交之后	收尾：日志记录、规范更新、归档
来源: README.md, start.md, workflow.md

前置条件与安装
Trellis 要求 Node.js ≥ 18 和 Python ≥ 3.9（用于钩子脚本和自动化层）。Python 会被自动检测——在 Windows 上，Trellis 会按顺序探测 python、python3 和 py -3。你可以使用 TRELLIS_PYTHON_CMD 覆盖检测，或使用 TRELLIS_SKIP_PYTHON_CHECK=1 完全跳过检测。

# 全局安装
npm install -g @mindfoldhq/trellis@beta
 
# 在你的仓库中初始化
trellis init -u your-name
来源: README.md, init.ts

接下来去哪
既然你已经了解了 Trellis 是什么以及它的各层如何协同，以下是文档的推荐阅读路径：

快速入门 — 安装 Trellis 并端到端运行你的首个任务
Trellis 目录结构 — 深入了解 Trellis 创建的每个文件和目录
架构概览 — 配置器模式、平台注册表和模板系统的内部工作原理
规范层与指南 — 如何编写和维护高效的规范
任务生命周期与 PRD — 从创建到归档的完整任务生命周期