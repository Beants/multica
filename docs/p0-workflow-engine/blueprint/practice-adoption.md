# 蓝图 · 实践采纳清单

> 覆盖 reference/ 五项目 + docs/ 三份方法论文档。每条：采纳 / 暂缓 / 不采纳 + 理由 + 目标阶段（P0-P3）。

## 1. docs/ 方法论文档

### 1.1 AI 军团文（multica-ai-army-loop-engineering.md）——蓝图主干，全量采纳

| 实践 | 决策 | 阶段 | 理由/落地形式 |
|---|---|---|---|
| 三根骨架（Agent 可调度/工作可编排/外部可交接） | 采纳 | P0-P2 | 蓝图主干：支柱 2/3 + 入站 Hook/出站 webhook |
| 准出字段 + Submission + Verdict | 采纳 | P0 | design.md §2 支柱 1/5，§3 实体 |
| Fan-out 并行与 AND 收敛 | 采纳 | P1 | fan_out/converge 节点类型 |
| End Node + 通知 + Acceptance + 定向返工 | 采纳 | P0 | design.md §2 支柱 5/6 |
| blocked/timeout/Sweeper 自愈 | 采纳 | P1 | workflow 级 Sweeper，扩展现有 runtime sweeper |
| 失败诊断七要素 | 采纳 | P1 | P1-6 诊断视图 |
| 指标四层（Agent/工作流/节点/场景） | 采纳 | P2 | P2-3/P2-4 指标聚合与大屏 |
| 四类场景（标准需求/Bug Fix/自迭代/历史问题池） | 采纳 | P0/P2 | 标准需求 P0；其余 P2 按序开通 |
| Temporal 工作流引擎 | 暂缓 | P3 | 决策 D4：自研先行，Temporal 语义接口预留，P3-1 评估 |
| 节点画布（Dynamic Config） | 暂缓 | P3 | TS-3：表单先行，复杂度阈值触发 |
| Agent 标准协议（CLI MCP） | 采纳 | P0/P3 | TS-4：CLI 扩展 P0 先行，P3 正式化 |
| 指标分析 Agent | 采纳 | P2 | P2-10 |
| AI 原生工作流（路径 B） | 暂缓 | P3 | 依赖 P2 指标数据；引擎与节点类型预留扩展性 |
| §6 人的位置（控制面/建设者上移）+ Loop Engineering 范式 | 采纳 | P0 | 平台角色设计原则：验收/熔断/升级入口都给人留位置；人是控制面，Agent 是执行面，工作流是协调层 [review 补录] |
| 事件接入与状态同步标准化（下一阶段 6 项之一） | 采纳 | P0/P2 | 入站 Hook 统一接入（P0-6）+ 出站 webhook 状态同步（P2-2）[review 补录] |

### 1.2 Harness Engineering 科普文（harness-engineering-explained.md）

| 实践 | 决策 | 阶段 | 理由/落地形式 |
|---|---|---|---|
| 六支柱作为系统划分框架 | 采纳 | P0 | 决策 D2，蓝图组织框架 |
| 生产与验收分离（generator/evaluator） | 采纳 | P0 | Evaluator 独立角色出 verdict；Executor 不自评终态 |
| 带环境的验证（Evaluator 实际操作产物） | 采纳 | P1 | gate 节点 agent 形态 + daemon 执行（TS-5） |
| 检查结果带修复提示（fix_hint 回灌） | 采纳 | P1 | gate 输出契约含 fix_hint（TS-5） |
| 渐进式披露（AGENTS.md ~100 行目录页） | 采纳 | P1 | Rules/Skills 分层注入：元数据常驻、指令触发加载、资源按需；节点 config 声明所需资产 |
| Context Reset（换干净 Agent 交接） | 采纳 | P0 | 天然成立：每个 StepInstance 是独立 AgentTask，新任务=干净上下文，交接靠 exit_fields/rework_context |
| 架构约束写成机器可执行规则 | 采纳 | P1 | Rules hard 级 + rules gate |
| 后台 Agent 扫描腐化 + 质量分 + 自动修复 PR | 暂缓 | P2/P3 | 与历史问题池场景合并考虑；需 P2 指标基础 |
| 文档园丁 Agent（文档新鲜度 CI 校验） | 暂缓 | P2 | 与知识健康检查（P2-6）合并 |

### 1.3 团队落地规范文（harness-engineering-team-ai-coding-spec.md）

| 实践 | 决策 | 阶段 | 理由/落地形式 |
|---|---|---|---|
| 约束三级（hard 红线/soft 约束/safety 兜底） | 采纳 | P1 | rule.level 字段（design.md §3 902 表） |
| 评估四层（L1 语法/L2 逻辑/L3 规范/L4 架构） | 采纳 | P0 | verdict.evidence 分层记录 |
| 3+1 Phase（Planner→Generator→Evaluator→Archiver） | 采纳 | P0 | 标准需求模板的角色设计参照；Archiver 职责由 event_store + 知识沉淀池承担 |
| team-harness 仓库（Rules/Skills 单一来源 + 同步） | 采纳 | P1 | 平台内 Rules/Skills 资产即单一来源；同步语义改为平台下发（daemon 注入工作目录） |
| Rules 三层体系（User/Team/Project） | 采纳 | P1 | rule.scope（workspace/project/agent） |
| harness-audit（规范可执行自检） | 暂缓 | P2 | 平台自身合规自检；需 Rules 资产先行 |
| 协作红线（先 Spec 后 Code 等） | 采纳 | P0 | 吸收为工作流模板结构约束（无评审节点不放行实现节点） |
| 反模式清单 | 采纳 | P0 | 作为模板设计与运营指南，非平台功能 |
| MCP 接入决策树/清单 | 采纳 | P1 | 复用现有 agent.mcp_config；决策树作为运营指南 |

## 2. reference/ 项目

### 2.1 ai-native-trellis

| 实践 | 决策 | 阶段 | 理由/落地形式 |
|---|---|---|---|
| 四阶段生命周期 + 子智能体派发（Plan/Implement/Verify/Finish） | 采纳 | P0 | 标准需求模板主干参照；子智能体派发=平台节点派发 |
| 证据分级（[已验证]/[推测]） | 采纳 | P0 | verdict.evidence + 蓝图自身写作规范；Agent 提交物要求证据标注 |
| 10 质量门禁（Evidence and Quality Gates） | 采纳 | P1 | gate 节点的 rules/script 检查项设计参照 |
| spec 分层体系（guides/backend/frontend + index） | 采纳 | P1 | Rules 资产的组织方式参照 |
| channel 运行时（类型化消息/worker spawn/线程讨论） | 暂缓 | P3 | Agent 间直接通信需求待验证；P0-P2 用平台中介（Issue/Comment/Step）即可 |
| trellis mem（跨会话记忆搜索） | 暂缓 | P2 | 与知识沉淀池（P2-5）合并评估 |
| 17 平台适配器 | 不采纳 | — | 维护负担；平台只需 daemon 一条通道（multica 已有 14 provider backend） |

### 2.2 orca

| 实践 | 决策 | 阶段 | 理由/落地形式 |
|---|---|---|---|
| worktree 隔离（每 Agent 会话独立 worktree） | 采纳 | P1 | fan-out 并行修改同仓库的隔离方案；daemon 侧实现，复用其 preflight 检查思想（脏 worktree 不静默删除） |
| 派发 preamble（注入通信协议指令） | 采纳 | P0 | 节点任务 prompt 结构：任务上下文 + 准出字段 schema + CLI 协议指令 + "提交恰好一次"约束 |
| 心跳监控 + hung 阈值 | 采纳 | P1 | workflow 级 Sweeper 的 running 超时检测参照（5min 心跳/10min hung 思路） |
| 类型化消息协议（status/dispatch/worker_done/escalation） | 采纳 | P0 | Submission/Verdict 字段设计参照；escalation → blocked 语义 |
| worktree drift 检测（base 落后阈值拒绝派发） | 采纳 | P1 | 实现类节点派发前检查，防止在陈旧代码上工作 |
| decision gates（显式 approval checkpoint） | 采纳 | P0/P1 | 由 acceptance 节点类型表达：run 级终验（P0）+ 流程中途显式审批检查点（P1 模板可插）；与 orca 的 decision_gate 消息语义同构 [review 补录] |
| SQLite 编排状态 | 不采纳 | — | 平台用 PostgreSQL（与 multica 一致） |
| Electron/PTY/computer-use/移动端 | 不采纳 | — | 决策 D1：Web 平台，不做桌面壳与桌面自动化 |

### 2.3 CodeStable

| 实践 | 决策 | 阶段 | 理由/落地形式 |
|---|---|---|---|
| 机器可执行 DoD 门禁（checklist YAML → 子进程 → 结构化 gate 结果） | 采纳 | P1 | script gate 的直接实现参照（TS-5） |
| 风险分级路由（Quick/Standard/Goal） | 采纳 | P1 | 模板选择维度：入站 Hook 按工作类型/风险匹配 template_key；简单工作走短模板 |
| skill 隔离（独立安装单元，共享走项目级 reference 目录） | 采纳 | P1 | Skills 资产自包含；跨 skill 共享经平台知识库引用，不互相读文件 |
| 独立 reviewer 强约束（review 必须独立 agent 执行） | 采纳 | P0 | 已由"生产与验收分离"+ verdict 写入权限契约覆盖（executor token 不可写 verdict，design.md §2 支柱 5）[review 补录] |
| 结果导向 skill 评测（seed repo + 隐藏测试 + 对照组） | 暂缓 | P3 | 成本高；P2 先用路由准确率 + 场景通过率等轻量指标 |
| cs-feedback 事件管道 | 不采纳 | — | 平台内反馈走 Acceptance/rework 机制，无需独立管道 |

### 2.4 cairn

| 实践 | 决策 | 阶段 | 理由/落地形式 |
|---|---|---|---|
| 六级知识质量阶梯（写入前过滤） | 采纳 | P2 | 知识沉淀池（P2-5）的提取过滤器 |
| 单一知识源 + 薄 agent 适配层 | 采纳 | P1 | Rules/Skills 存平台 DB 为单一来源；daemon 注入工作目录为薄适配 |
| 待沉淀池（标记→批量提取→成熟度追踪） | 采纳 | P2 | P2-5；成熟度 5 级 draft/verified/proven/stale/**conflict** [review 修正：原写 4 级漏 conflict] |
| 强制触发信号（沉淀入口约束） | 采纳 | P2 | P2-5 入口：只有命中触发信号的候选才进沉淀池，防低质量候选淹没 [review 补录] |
| 双因子健康检查（时间 + 代码变更） | 采纳 | P2 | P2-6；类名提取正则需泛化（原实现是 Java 特定） |
| 注入消毒（自由文本回放前清洗） | 采纳 | P1 | rework_context/exit_fields 注入 Agent 上下文前消毒，防存储型注入 |
| L1-L4 文件名前缀分层 | 不采纳 | — | 用 metadata/level 字段而非文件名约定 |
| Kiro 特定 hook 格式 / Graphify 扩展 | 不采纳 | — | agent 特定/收益边际 |

### 2.5 LiveAgent

| 实践 | 决策 | 阶段 | 理由/落地形式 |
|---|---|---|---|
| skill 访问策略（会话级白名单 + 内置保护 + 操作分级） | 采纳 | P1 | 节点任务只注入绑定 skills；平台内置 skills 标记只读；防 Agent 越权读写 |
| MCP per-server 调用锁 | 采纳 | P1 | daemon 侧实现：同一 MCP server 的并发调用串行化，防状态错乱 |
| Gateway 纯协议中继（不碰本地状态/凭证） | 采纳 | P0 | 既有架构原则确认：server 不碰 daemon 本地文件系统，只收发协议消息 |
| Segment + Summary 历史压缩 | 暂缓 | P2 | 长任务上下文管理；与 Context Reset 策略合并评估 |
| Tauri 桌面壳 / ClawHub 市场 | 不采纳 | — | 决策 D1 Web 平台；skill 分发走平台内资产 |

## 2.6 multica harness/（仓内 prompt 层原型）

harness 机制的采纳映射不在本清单展开，已单列于 **design.md §5**（5.1 pipeline YAML 层 / 5.2 squad-briefing 运行契约层 / 5.3 gates+skills+cli 运营层，逐条核对来源）。关键采纳：两层状态模型→DB 实体取代、verdict/decision 命名铁律、Spec Freeze→acceptance 节点、test_plan 项目级配置、adversarial 第 4 门禁形态、auto_pass 验收、scar 聚合、skill sync+hash pinning（P2）[review 后大幅补全]

## 3. 汇总统计

| 决策 | 数量 | 说明 |
|---|---|---|
| 采纳 | 50 | 全部映射到 P0-P2 具体交付物（含 review 后补录 5 条） |
| 暂缓 | 10 | 均有明确触发条件与目标阶段（多为 P3 或依赖 P2 数据） |
| 不采纳 | 7 | 均与已确认决策冲突（D1 Web 平台/D5 内部工具）或收益边际 |
