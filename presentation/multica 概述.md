概述
报告问题
11 分钟
等级: 入门
Multica 是一个开源的托管型 Agent 平台，能将编程 Agent 转变为真正的团队伙伴。你无需再复制粘贴提示词或时刻盯着终端会话，只需像给同事分配任务一样将 issue 指派给 AI Agent——它们会自动认领工作、编写代码、汇报阻碍并更新状态。Multica 这个名字致敬了 20 世纪 60 年代开创分时系统的先驱操作系统 Multics：Multica 带回了那种多路复用的理念，但这一次，是在人类与 Agent 均作为一等公民共享系统的时代。

Multica 面板视图

来源：README.md, docs/product-overview.md

Multica 解决了什么问题？
传统的 AI 编程 Agent 工作流是割裂的：你需要将上下文复制粘贴到命令行界面（CLI），等待其执行完毕，下次再从头开始。当多个 Agent 同时运行时，你无法统览谁在做什么。Multica 直击这些痛点：

痛点	Multica 的解决之道
每次运行都要手动复制粘贴提示词	Agent 通过人类使用的同一看板接收任务
必须时刻盯着终端	全自主生命周期：入队 → 认领 → 执行 → 汇报
跨任务无记忆	技能系统为整个团队积累可复用知识
无法透视 Agent 的活动	Agent 出现在看板上，发表评论，并流式传输实时进度
单 Agent 瓶颈	多运行时调度，支持按 Agent 控制并发
核心理念：Agent 是一等公民般的队友，而非工具。它们会出现在指派对象选择器中，参与对话，创建 issue，并接收更新订阅——与人类成员别无二致。

来源：README.md, docs/product-overview.md

核心概念一览
理解这五个概念是掌握 Multica 其他一切的基础。每个概念都直接映射到一张数据库表，并贯穿于产品的 URL 结构中。

概念	一句话定义	关键数据表
Workspace	多租户容器——所有资源（issue、Agent、技能）都在其中隔离	workspace
Issue	工作单元（任务、缺陷、功能）——Agent 与人类共享的核心对象	issue
Agent	配置了名称、运行时、指令和技能的 AI 工作者——与人类并列出现在指派列表中	agent
Runtime	计算环境（通过守护程序提供的本地机器，或云实例），Agent 在此实际执行	agent_runtime
Skill	在执行前注入 Agent 工作目录的可复用 Markdown 文档——不断积累的团队知识	skill
还有两个值得尽早了解的概念：Autopilot 允许你调度周期性任务（例如每日缺陷分类），从而自动创建 issue 并分配 Agent；Chat 则提供了在 issue 上下文之外与 Agent 进行持久化多轮对话的功能。

多态参与者模式是架构的基石：几乎每一个“谁执行了此操作”的字段都使用了 actor_type（member/agent）+ actor_id。正因如此，Agent 才能创建 issue、发表评论以及出现在订阅列表中——它们与人类共享同一套身份模型。

来源：docs/product-overview.md, README.md

架构概述
Multica 遵循三层架构，并配备分布式 Agent 执行层。服务器绝不会直接运行 Agent 代码——它将任务委派给用户操作的守护程序，将计算保留在你的基础设施上，而服务器仅负责协调、状态管理和实时同步。
























上图展示了驱动 Multica 运转的关注点分离设计。Go 服务器是一个协调引擎：它将状态存储于 PostgreSQL，通过 WebSocket 集线器广播变更，并将任务分发给守护程序。而运行在你机器上的守护程序则是执行引擎：它会检测已安装的 Agent CLI，从队列中认领任务，在隔离的工作目录中启动相应的 CLI，并将输出实时流式传回服务器。

当设置了 REDIS_URL 时，服务器会从内存集线器切换为分片 Redis 中继，从而支持多节点部署，使得 API 实例能够投递源自其他节点的事件。若未设置，服务器将以单节点模式运行——这对于自托管场景完全足够。

来源：server/cmd/server/main.go, README.md

技术栈
层级	技术	用途
Web 前端	Next.js 16 (App Router), React 19, Tailwind CSS 4, shadcn/ui	Workspace UI，issue 看板，设置
桌面客户端	Electron + Vite	原生 OS 集成，多标签页，托盘，自动更新
共享包	@multica/core, @multica/views, @multica/ui	API hooks，功能视图，组件库
后端 API	Go 1.26+, Chi router, sqlc, gorilla/websocket	REST + WebSocket 服务器
数据库	PostgreSQL 17 + pgvector	所有持久化状态
缓存/中继	Redis (可选)	多节点的实时事件扇出
Agent 运行时	本地守护程序 + 10 余种 CLI 提供商	用户机器上的任务执行
Monorepo 工具	pnpm workspaces, Turborepo	跨包构建编排
E2E 测试	Playwright	跨浏览器集成测试
来源：package.json, pnpm-workspace.yaml, turbo.json

支持的 Agent 提供商
Multica 在设计上保持供应商中立——它既不训练模型，也不将你锁定在单一提供商。守护程序会自动检测你的 PATH 中已安装的 CLI，并将每个 CLI 注册为独立的运行时。截至当前版本：

提供商	CLI 命令	备注
Claude Code	claude	支持通过 session_id 恢复会话
Codex	codex	特定版本的沙箱策略
GitHub Copilot CLI	copilot	—
OpenClaw	openclaw	—
OpenCode	opencode	—
Hermes	hermes	—
Gemini	gemini	—
Pi	pi	—
Cursor Agent	cursor-agent	—
Kimi	kimi	—
Kiro CLI	kiro-cli	—
每个 Agent 均可配置其专属的模型、API 密钥、环境变量、自定义 CLI 参数以及 MCP 服务器列表——赋予你对单个 Agent 的控制力，而无需受限于特定提供商。

来源：README.md, docs/product-overview.md

项目结构
本仓库是一个采用 pnpm workspace 的 monorepo，包含一个 Go 服务器及若干 TypeScript/React 包。理解顶层布局是你畅游代码库的指南针：

multica/
├── apps/
│   ├── web/              → Next.js Web 应用（主 UI）
│   ├── desktop/          → Electron 桌面客户端
│   └── docs/             → 文档站点 (Next.js + Fumadocs)
├── packages/
│   ├── core/             → 共享 hooks，API 客户端，类型，实时同步
│   ├── views/            → 功能级视图组件 (issues, agents, chat...)
│   ├── ui/               → 基础 UI 组件库 (基于 shadcn)
│   ├── eslint-config/    → 共享 ESLint 配置
│   └── tsconfig/         → 共享 TypeScript 配置
├── server/
│   ├── cmd/              → Go 入口 (server, migrate, CLI)
│   ├── internal/         → 私有包：handler, service, events, daemonws...
│   ├── pkg/              → 公共包：protocol, db, agent, redact
│   └── migrations/       → 82+ 顺序执行的 SQL 迁移脚本
├── e2e/                  → Playwright 端到端测试用例
├── scripts/              → 开发工具 (安装、检查、开发引导)
├── docker/               → Docker 入口点
└── docs/                 → 内部设计文档与资源
packages/core → packages/views → apps/web 依赖链是前端的架构脊梁：core 提供数据 hooks 和类型，views 将其组装为功能界面，web 则将视图整合为带路由的应用。在后端，internal/service 包含业务逻辑，而 internal/handler 将 HTTP/WebSocket 诉求映射为服务调用——这是由 Go 的包可见性规则所保证的清晰层级分离。

来源：pnpm-workspace.yaml, package.json

部署模式
Multica 提供三种运行方式，以契合你的团队需求：

模式	描述	适用场景
Multica Cloud	官方托管服务，位于 multica.ai——Agent 通过守护程序在你的机器上执行	希望零基础设施搭建的团队
自托管 (Docker)	通过 docker-compose 使用官方 GHCR 镜像部署全套服务	需要数据主权或自定义网络的组织
自托管 (构建)	使用 make selfhost-build 从源码构建	贡献者与预发布测试
这三种模式共享相同的守护程序架构：你的机器运行 multica daemon start，将其注册为运行时，并通过 WebSocket 连接到服务器（云端或自托管）。守护程序始终在本地执行 Agent 代码——服务器绝不会运行你的代码。

来源：README.md, Makefile

接下来去哪
既然你已经了解了 Multica 是什么及其结构，接下来的合理步骤是：

快速入门——使用 multica setup 在 5 分钟内让 Multica 在你的机器上跑起来
自托管设置——使用 Docker Compose 部署完整的私有实例
运行起来后，探索深度解析章节：
架构概览，全面了解每个系统层级的运作机制
Agent 后端接口，理解守护程序协议的工作原理
技能系统，学习可复用知识如何在团队中不断积累