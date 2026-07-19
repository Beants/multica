# harness 机制清单（P0 前置）

> 全量提取 `multica/harness/` 机制，逐条标注 **P0 必需 / P1 补充 / 不迁移** + 平台原语映射。
> 标注口径：P0 必需 = 缺了 P0 出口标准跑不通；P1 补充 = P0 能跑但六类能力需要它；不迁移 = prompt 层变通（平台原生取代）或明确不需要。
> 平台原语引用蓝图：`.trellis/tasks/archive/2026-07/07-17-agent-ide-blueprint/design.md`（下称"蓝图 §X"）。
> 与蓝图 §5 的差异汇总见文末 §7。

## 1. squad-briefing.md（运行契约核心，195 行）

| # | 机制 | 锚点 | 标注 | 平台原语映射 / 理由 |
|---|---|---|---|---|
| 1.1 | 确定性归平台/脚本，认知归 AI | :11-17 | **P0 必需** | 引擎=确定性推进器的存在理由；Agent 只做语义判断。蓝图设计哲学，写入引擎注释与节点类型划分依据 |
| 1.2 | parent issue + staged child + stage 屏障自动唤醒 | :21 | 不迁移 | 平台原生取代：workflow_run + step_instance + 引擎事件驱动推进。harness 用 issue.stage 整数屏障正是因为没有工作流引擎 |
| 1.3 | 标准流水线 **7 阶段**（规划→规划门禁→★Spec Freeze→实现→基线门禁→API 门禁→审查→验收） | :25-34 | **P0 必需** | standard 模板种子。**注意：squad-briefing 是 7 阶段（API 门禁独立成阶段 5），standard.yaml 只有 6 阶段——以 squad-briefing 为准**（差异 D-1） |
| 1.4 | Spec Freeze = assignee-swap（非 stage） | :36 | **P0 必需** | acceptance 节点（中途人工关卡）：进入即暂停自动推进；frozen_spec/frozen_test_cases 标志落 acceptance / run.context。已在蓝图 §5.2 |
| 1.5 | baseline `--exclude api` 拆分（证据互不污染） | :38 | P1 补充 | gate 节点 config 的 commands/exclude 语义；每个 gate 独立证据文件原则 |
| 1.6 | API 测试前置（审查之前，打回 1.8→0.4） | :40 | P1 补充 | 模板节点顺序设计原则：便宜的验证放在贵的审查前 |
| 1.7 | Bugfix 流水线 4 阶段（无 Spec Freeze、无独立 API 门禁） | :42-51 | **P0 必需** | bugfix 模板种子（蓝图 P0-8 已含）。（差异 D-12：bugfix.yaml 阶段构成/auto_pass 与 briefing 矛盾，以 briefing 为准） |
| 1.8 | 两层状态模型（parent metadata KV + child verdict 评论 / task.json + gate-result.jsonl） | :55-82 | 不迁移 | 平台 DB 取代：run.context（仅引擎写）+ submission/verdict 实体。已在蓝图 §5.2 |
| 1.9 | 不用 pipeline-state.yaml 的理由（GC 清理/无并发保护/无脚本消费） | :72 | 不迁移 | 设计约束记录：平台状态必须 DB 化 |
| 1.10 | metadata 类型键（--type bool/number，默认字符串易踩坑） | :69 | **P0 必需** | run.context 用 JSONB（天然带类型，优于 harness 字符串 KV）；API 文档写明类型语义 |
| 1.11 | 6 角色表（队长/规划员/实现员/审查员/门禁执行器/人类 approver） | :88-97 | **P0 必需** | 模板角色槽 + agent_selector；human approver = acceptance.reviewer。已在蓝图 §5.2 |
| 1.12 | 铁律：下游不可修改上游产物 | :99 | **P0 必需** | 引擎规则：rework 定向回上游节点重做；下游 step 对上游 submission 只读 |
| 1.13 | 准出协议字段：status(DONE/DONE_WITH_CONCERNS/BLOCKED/NEEDS_CONTEXT) + verdict + artifacts + root_cause + confidence + **gaps** | :103-115 | **P0 必需** | submission/verdict 实体。**差异 D-2：蓝图 submission/verdict 均缺 status 四态与 gaps 字段——需补** |
| 1.14 | 命名铁律：verdict（pass/fail/blocked）≠ decision（APPROVED/CONDITIONAL/REJECTED） | :125 | **P0 必需** | 已在蓝图 §5.2；API 字段命名红线 |
| 1.15 | 交接矩阵（载体×写者×读者） | :131-140 | **P0 必需** | 平台实体写入权限矩阵：每类状态单一写者（引擎写 run/step，执行者写 submission，gate/evaluator 写 verdict，人写 acceptance） |
| 1.16 | 门禁三层硬度：硬(Script)/半硬(Hybrid)/软(Soft) | :146-152 | P1 补充 | gate 节点 hard 标志 + **hybrid 形态（script 备料 + agent 判定）——蓝图四形态之外的组合模式，见差异 D-5** |
| 1.17 | 硬门禁 = 事实 + 处置（脚本出事实不可推翻；agent 逐条处置 fatal/flaky/历史/acceptable） | :154 | P1 补充 | **gate_run.output 两层结构：facts（脚本客观输出）+ dispositions（agent 分类处置）。差异 D-4** |
| 1.18 | 对抗性交付审查（新鲜上下文，不给 prd/design） | :156-158 | P1 补充 | 已在蓝图（adversarial 第 4 形态） |
| 1.19 | 熔断：rollback_counter.py 计数，队长不自己数 | :160-162 | **P0 必需** | 引擎读 step_transition 计数。已在蓝图 |
| 1.20 | 测试用例两段式：业务侧（规划员产，Spec Freeze 冻结）+ 技术侧（实现员补充） | :166-170 | P1 补充 | 节点 exit_fields schema：规划节点产 business-test-cases（acceptance 冻结），实现节点补 tech-test-cases |
| 1.21 | 铁律：写测试的人不执行测试 | :172 | **P0 必需** | **gate 节点 agent_selector 必须 ≠ 上游 executor（平台硬约束，不只 prompt 约定）。差异 D-6** |
| 1.22 | 测试职责矩阵（用例/自动化/执行/报告 四列分工） | :176-181 | P1 补充 | 模板角色分工参照 |
| 1.23 | 官方门禁执行全归门禁执行器（否则等于自审） | :183 | **P0 必需** | 同 1.21，平台约束 |
| 1.24 | E2E 例外：最终端到端验收归人类 | :185 | **P0 必需** | acceptance 节点（Agent completed ≠ business completed）。已在蓝图 |
| 1.25 | test-plan.json（项目级测试命令声明，cmd:null=SKIP） | :187-189 | P1 补充 | 已在蓝图 §5.2（项目级 test_plan 配置） |
| 1.26 | REFLECT：验收后 autopilot 定时触发反思，产出写 guides/，不阻断 | :193-195 | P1 补充 | 知识沉淀池（P2-5）触发方式参照：验收通过事件 → autopilot → 反思 agent → 沉淀池 |

## 2. leader/leader-prompt.md（队长行为契约，140 行）

| # | 机制 | 锚点 | 标注 | 平台原语映射 / 理由 |
|---|---|---|---|---|
| 2.1 | 队长=确定性推进器（相同状态→相同动作），不轮询、不编排 | :9,11,140 | **P0 必需** | 引擎角色定位：只做状态推进，不做语义判断 |
| 2.2 | UUID 缓存，禁用 fuzzy name 指派 | :25-33 | **P0 必需** | agent_selector 在模板 publish 时解析固化为 UUID，运行时不做 fuzzy 解析。**差异 D-7** |
| 2.3 | 最多只预创建下一阶段 backlog，后续阶段屏障闭合后动态创建 | :84 | **P0 必需** | **引擎激活策略：step_instance 按需创建（当前 active + 下一节点 pending），不全图预建。差异 D-3** |
| 2.4 | verdict 驱动推进：pass→推进；fail/blocked→回退 | :86-93 | **P0 必需** | 引擎核心推进逻辑（蓝图 §4.1 已覆盖） |
| 2.5 | 回退流程：原上游 child 提回 todo（不建新 child）+ 评论附 root_cause | :110-121 | **P0 必需** | 引擎 rework 语义：同 node_key 新 attempt（UNIQUE(run_id,node_key,parent,attempt)，蓝图 §8），rework_context 注入 |
| 2.6 | 熔断：读 metadata 计数比对阈值，不自己数 | :123-132 | **P0 必需** | 引擎读 step_transition。已在蓝图 |
| 2.7 | 评论不会自动注入 resume prompt，agent 须主动读 | :108 | **P0 必需** | **rework_context 必须由 daemon BuildPrompt 显式注入任务 prompt，不能假设 agent 自己去读。差异 D-8（强化既有设计）** |
| 2.8 | 首次唤醒初始化：判定 standard/bugfix → 写 pipeline/current_stage → 创建首阶段 child + 预创建下一阶段 | :71-82 | **P0 必需** | 引擎 Run 初始化语义：StartRun 时判定模板、初始化 run.context（pipeline/current 状态）、激活首节点（+ 预创建下一节点，同 2.3） |

## 3. gates/（13 脚本）

| # | 机制 | 锚点 | 标注 | 平台原语映射 / 理由 |
|---|---|---|---|---|
| 3.1 | baseline.py：B−A diff 语义（new/known/resolved，只 block 新增） | baseline.py:211-229 | P1 补充 | gate 脚本语义规范（已在蓝图 §5.2）；script gate 种子 |
| 3.2 | baseline.py：test-plan 加载 + cmd:null 跳过 + pnpm 默认模板回退 | baseline.py:259-284,297-312 | P1 补充 | test_plan 配置 + gate 默认命令 |
| 3.3 | baseline.py：600s 超时 exit 124 + UNKNOWN_FALLBACK 截取 200 字符 | baseline.py:32,110,127,136 | P1 补充 | gate 安全契约的超时/输出上限默认值参照 |
| 3.4 | api_gate.py：api 键独立门禁 + 无键 SKIP（不阻断） | api_gate.py:143-145 | P1 补充 | gate 执行结果需有 SKIP 状态（区别于 pass） |
| 3.5 | gate_result.py：append-only 证据事件流（12 字段：schema/ts/task/phase/gate/command/status/duration_ms/hard/summary/evidence/new_failures） | gate_result.py:106-119 | **P0 必需** | gate_run 表 + step_transition 的字段 schema 参照；append-only 原则（证据永不覆盖） |
| 3.6 | rollback_counter.py：per-phase 计数 + 阈值 exit 2 + 换 phase 重置 | rollback_counter.py:87-107 | **P0 必需** | 引擎熔断计数语义参照（同节点连续 rework 计数，节点变更重置） |
| 3.7 | scar_summary.py：warn/skipped 聚合为"伤疤块"验收时暴露 | scar_summary.py:44-83 | P1 补充 | 验收视图聚合 soft findings（已在蓝图 §5.2） |
| 3.8 | plan_contract_check.py：PRD 6 节 + AC checkbox 检查（advisory） | plan_contract_check.py:21-64 | P1 补充 | rules gate 检查项种子 |
| 3.9 | delivery_checklist.py：7 源交叉交付审计（task.json/artifacts/jsonl/gate-result/baseline/verification-contract/research） | delivery_checklist.py:76-220 | P1 补充 | 交付前 checklist gate 种子（P1/P2） |
| 3.10 | detect_tests.py：扫 Makefile/package.json/go.mod 等生成 test-plan 草稿 + check STALE | detect_tests.py:64-195 | P1 补充 | test_plan 配置引导工具 |
| 3.11 | gate_prd_confirm.py：人工确认写 meta.prd_confirmed（ISO8601） | gate_prd_confirm.py:121-123 | **P0 必需** | acceptance 决策记录：谁、何时、确认了什么（acceptance.reviewer_id/decided_at + payload 摘要） |
| 3.12 | spec_freshness.py：双条件 staleness（mtime 超阈值 AND 老于最近 task 活动） | spec_freshness.py:49 | P1 补充 | 知识健康检查（P2-6）参照 |
| 3.13 | task_resolver.py：任务标识解析单点 + 严格后缀匹配 + 逃逸防护 | task_resolver.py:39-46,121-135,138-229 | 不迁移 | 平台 API 层用 UUID，无文件系统解析需求 |
| 3.14 | verification_contract_check.py：REQ/BC/TC 追溯链 + 5 覆盖率指标 + freeze/spec_revision 一致性（执行结果须对应当前 spec_revision） | verification_contract_check.py:13-291 | P1 补充 | 可追溯性契约（需求→用例→执行→证据）；P2 指标层吸收，P1 可选 |
| 3.15 | workflow_integrity_check.py：workflow.md 结构自检 | workflow_integrity_check.py:121-161 | 不迁移 | 平台模板 publish 校验取代（JSONLogic schema 校验等） |

## 4. guides/（11 份方法论）

| # | 机制 | 锚点 | 标注 | 平台原语映射 / 理由 |
|---|---|---|---|---|
| 4.1 | plan_sha256 / workspace_sha256 指纹（plan 变更使 before 快照失效；workspace=git HEAD+内容 hash） | baseline-and-gate-result-protocol.md:55,131-136 | P1 补充 | gate 证据时效校验：before 快照与代码指纹绑定；template snapshot 已在蓝图 |
| 4.2 | **forward-compat 规则**：missing file=unknown 不是 failed；容忍未知字段；不丢未知字段；breaking change 升 schema 版本 | baseline-and-gate-result-protocol.md:322-329 | **P0 必需** | **平台所有 JSONB 字段/事件/gate 输出/准出 schema 的演进规则。差异 D-9（平台协议原则，蓝图未写）** |
| 4.3 | dashboard-evidence-contract：4 接口 + staleness 规则 + 缺失容忍（missing=零值/null 不是异常） | dashboard-evidence-contract.md:38-146 | P1 补充 | 观测大屏（P2-4）数据契约参照 |
| 4.4 | 10 质量门禁（spec 完整性/来源覆盖/架构影响/任务分解/测试证据/review 证据/anti-slop/证据响应/secrets/context 更新） | evidence-and-quality-gates.md:5-135 | P1 补充 | rules gate 检查项库（已在蓝图 practice-adoption） |
| 4.5 | executor-protocol：证据分级 + 推测传播规则 + 16 反模式 + Human Gate 类别（commit/push/PR 显式授权、test↔prod 环境切换） | executor-protocol.md:22-35,120-136,141-266 | P1 补充 | 节点 instructions 模板素材；证据分级已在蓝图采纳；Human Gate 类别对应蓝图 safety 级规则 |
| 4.6 | methodology-ownership：ownership 矩阵 + 单一 owner 原则 | methodology-ownership.md:7-27 | P1 补充 | 实体写入权限矩阵设计原则（每类状态单一写者） |
| 4.7 | plan-artifact-contract：状态边界（plan-state ≠ task lifecycle，各有单一真相源） | plan-artifact-contract.md:34-48 | P1 补充 | run.status 与 issue.status 边界规则：不互相镜像，各有真相源 |
| 4.8 | script-first 决策树：能 exit code 表达→脚本；结构化输入→脚本；脚本备料+模型判定→hybrid；否则→纯 prompt | script-first-architecture.md:70-83 | **P0 必需** | 节点类型划分依据（agent/gate/hybrid 的判定标准）；与 1.1 互证 |
| 4.9 | tool-interaction-contract：canonical/advisory 表 + 文件写权限 | tool-interaction-contract.md:149-203 | P1 补充 | 实体写入权限矩阵（同 4.6） |
| 4.10 | traceable-harness-contracts：条件 DAG（depends_on/touches/global_changes 声明）+ Join 阶段（merge/publish/最终验证/commit 是收敛后操作） | traceable-harness-contracts.md:32-33 | P1 补充 | **fan-out 设计输入：并行子任务须声明 depends_on/touches/global_changes；共享入口/协议/schema/DB 走串行；merge/publish/commit 是 Join 操作。差异 D-10** |
| 4.11 | trellis-extension-points：5 扩展点 | trellis-extension-points.md:14-89 | 不迁移 | Trellis 自身扩展机制，与平台无关 |
| 4.12 | evidence-plan.json 契约（required/not_applicable 双模式、命令 id 稳定、拒绝空跑命令、canonical JSON→SHA）+ evidence-first 激活时序（before 证据先于状态翻转、指纹一致才复用且绝不覆盖、缺 before 阻断交付） | baseline-and-gate-result-protocol.md:27-55,170-247,318-320 | P1 补充 | gate 证据的反作弊与时序约束：gate_run/submission 证据时效设计输入；not_applicable 单条 skipped 事件即完整证据 |
| 4.13 | guides/index.md：任务执行层 vs 流水线编排层正交的双层定位 | index.md:6-12 | P1 补充 | 心智模型参照：引擎（编排层）与任务执行层正交，与 1.1/2.1 互证 |

## 5. skills/（registry + 角色 prompt + 子 skill + sync 机制）

| # | 机制 | 锚点 | 标注 | 平台原语映射 / 理由 |
|---|---|---|---|---|
| 5.1 | registry.json：pinned SHA + multica_id 双字段（锁定与注册映射） | registry.json:6-101 | P1 补充 | Skills 资产版本锁定 + 平台注册（P2 skill sync） |
| 5.2 | 角色 prompt 硬禁令（implementer 不跑 baseline、reviewer 只评不改、planner 不碰代码） | implementer/prompt.md:66-71 等 | **P0 必需** | 节点 instructions 模板 + 平台权限契约（executor token 不可写 verdict 已覆盖部分；行为禁令进 instructions） |
| 5.3 | gate-runner 两铁律：事实不可推翻 + 处置归我（fatal/flaky/historical/acceptable 四分类）；gates/ 脚本只跑不改（预置工程资产） | gate-runner/prompt.md:13-57,32,123 | P1 补充 | gate agent instructions 模板核心；gate_run facts/dispositions 结构（同 1.17）；"只跑不改"对应蓝图门禁安全契约的脚本来源 allowlist |
| 5.4 | reviewer prompt：decision 字段 + soft gate 永不阻断 + REJECTED 写 root_cause | reviewer/prompt.md:23,41-52 | P1 补充 | review 节点模板 |
| 5.5 | 子 skill 8 个 + frontmatter description 即渐进式披露触发条件 | 各 SKILL.md frontmatter | P1 补充 | Skills 资产种子内容；description=触发条件写入平台 skill 规范 |
| 5.6 | .source.json：repo/ref/commit + per-file sha256（SKILL.md 字节级一致，source 另存） | 各 .source.json | P1 补充 | skill 内容 hash + drift 检测（已在蓝图 §5.3） |
| 5.7 | sync_skills.py check：autopilot 定时 drift 检测（exit 1） | sync_skills.py:108-134 | P1 补充 | P2 skill 运营 |
| 5.8 | sync_agents.py：squad-briefing + role prompt 拼接写 agent.instructions，幂等全量覆盖 | sync_agents.py:102 | P1 补充 | 节点 instructions 组装参照（平台共识 + 角色指令分层） |
| 5.9 | register_skills.py：frontmatter 解析 + upsert + fuzzy bind | register_skills.py:63-184 | P1 补充 | skill 注册流程参照 |

## 6. cli/（4 工具）与跨源约束

| # | 机制 | 锚点 | 标注 | 平台原语映射 / 理由 |
|---|---|---|---|---|
| 6.1 | resume.py：work_dir-only 恢复（无 session_id）+ 磁盘存在守卫 | resume.py:230-235,432-437 | 不迁移 | 引擎即恢复语义（Run/Step 持久化 + Sweeper 重激活） |
| 6.2 | **workdir 24h GC 清理**（done issue workdir 定时删除） | squad-briefing.md:72 + resume.py 磁盘守卫 | **P0 必需** | **submission.artifacts 不能引用 workdir 相对路径作长期证据——重要产物必须落 DB / PR URL / 分支 / 持久附件。差异 D-11（影响 submission 设计）** |
| 6.3 | TC ID 约定（TC-001…）+ frozen_test_cases 列表 | squad-briefing.md:67（另见 leader-prompt.md:105） | P1 补充 | 准出字段的测试用例引用方式（exit_fields 中 cases 为 ID 列表） |

## 7. 与蓝图 §5 的差异汇总（AC3）

| # | 差异 | 内容 | 影响 |
|---|---|---|---|
| D-1 | 修正 | standard 模板应为 **7 阶段**（squad-briefing :25-34，含独立 API 门禁阶段 5），蓝图 §5.1 按 standard.yaml 写的 6 阶段。且 standard.yaml 与 squad-briefing 另有 3 处实质矛盾，均以 squad-briefing 为准：①Spec Freeze 时点——yaml:59-61 写 `after_stage: 1`（规划后即冻结），briefing :29,36 + leader :99 均为阶段 2（规划门禁）闭合后；②yaml:31 Implement 产 `baseline/after.json`（实现员跑快照），违反 briefing :30,:183（实现员不碰 baseline，快照全归门禁执行器）；③yaml:21-25 阶段 2 只有 plan_contract_check，briefing :28 阶段 2 还须 `baseline snapshot --phase before --exclude api`（冻结已知失败基线） | P0-8 种子模板以 squad-briefing 7 阶段为准；yaml 仅作结构参考 |
| D-2 | 新增 | submission/verdict 均缺 **status 四态**（DONE/DONE_WITH_CONCERNS/BLOCKED/NEEDS_CONTEXT）与 **gaps** 字段（squad-briefing :103-115） | 蓝图 §3 submission/verdict 表需补字段 |
| D-3 | 新增 | 引擎激活策略：**只预创建下一节点**（leader :84），不全图预建 | 引擎设计细节，蓝图未写 |
| D-4 | 新增 | gate_run.output 两层结构：**facts（不可推翻）+ dispositions（fatal/flaky/historical/acceptable）**（squad-briefing :154 + gate-runner prompt） | 蓝图 gate_run 表需补结构约定 |
| D-5 | 新增 | **hybrid（半硬）门禁形态**：script 备料 + agent 判定（squad-briefing :146-152 + script-first 决策树） | 蓝图四形态（script/agent/rules/adversarial）之外需支持 script+agent 组合 |
| D-6 | 新增 | **gate 节点 agent_selector ≠ 上游 executor** 平台硬约束（squad-briefing :172,183） | 蓝图写了 verdict 写入权限，未写 selector 约束 |
| D-7 | 新增 | agent_selector 在 **publish 时固化 UUID**，运行时不 fuzzy 解析（leader :25-33） | 模板 publish 流程细节 |
| D-8 | 强化 | rework_context 必须由 daemon **显式注入 prompt**（评论不会自动进上下文，leader :108） | 蓝图已有 rework_context，此条证明必须显式注入 |
| D-9 | 新增 | **forward-compat 规则**：missing≠failed、容忍未知字段、schema 版本（baseline-and-gate-result-protocol :322-329） | 平台协议演进原则，蓝图未写 |
| D-10 | 新增 | fan-out 须声明 **depends_on/touches/global_changes**；merge/publish/commit 是 Join 阶段操作（traceable-harness-contracts :32-33） | fan-out 节点 config 设计输入 |
| D-11 | 新增 | **workdir 24h GC**：artifacts 必须 DB/持久化，不能引用 workdir 路径（squad-briefing :72） | submission.artifacts 设计约束 |
| D-12 | 修正 | bugfix.yaml 与 squad-briefing 两处矛盾，以 squad-briefing 为准：①yaml:18-27 把 Implement+Baseline 合并为阶段 2 且 implementer 自跑硬门禁，违反"写测试的不执行测试"（briefing :172,:183 铁律；:42-49 是独立阶段 3 基线门禁、门禁执行器跑）；②yaml:42,48 的 `auto_pass`/`auto_pass_condition` 与 briefing :51（bugfix 验收走人类 member-暂停）冲突——**裁决（2026-07-18 用户确认）：auto_pass 保留为 acceptance 节点能力（默认关），bugfix 种子模板默认人工验收，成熟场景再开启**。已回写蓝图 roadmap P0-8 | P0-8 bugfix 种子模板阶段构成以 briefing 为准；auto_pass 定位已定 |

## 8. 统计

| 标注 | 数量 | 说明 |
|---|---|---|
| P0 必需 | 29 | 引擎/实体/权限/模板设计的硬输入 |
| P1 补充 | 38 | 门禁、知识资产、运营机制 |
| 不迁移 | 7 | prompt 层变通，平台原生取代 |

（两轮审查：第 1 轮 3 MAJOR + 16 MINOR——新增 2.8/4.12/4.13 三行、修正 10 处锚点、2 处计数、D-1 扩展、新增 D-12；第 2 轮 0 MAJOR + 5 MINOR——1.7 补 D-12 交叉引用、3.6/4.4/4.8 锚点边界、D-12 补引 :172,:183。全部修复并经子代理复核确认；总计 74 条）
