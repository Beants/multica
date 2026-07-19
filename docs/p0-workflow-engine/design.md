# P0 执行设计（R1 审查修订版）

> 只写蓝图未展开的执行细节；架构/实体/并发/fork 卫生以蓝图 design.md 为准（§2/§3/§6/§8），机制差异以清单 D-1~D-12 为准。本文不重复照抄。
> 本版已吸收第 1 轮审查的 2 CRITICAL + 10 MAJOR + 9 MINOR，修订点标注 [R1-fix]。

## 1. Migration 计划（900+ 号段）

| 文件 | 内容 |
|---|---|
| `server/migrations/901_workflow_core.up.sql` / `.down.sql` | 10 张表 + 索引 + UNIQUE 约束 |

10 张表（蓝图 §3 901 表 + 清单 D-2 补充 + R1 审查修订）：

- `workflow_template` / `workflow_node` / `workflow_edge`（edge.condition P0 只允许 NULL——线性推进；JSONB 列预留，P1 求值）
- **`workflow_hook`（第 10 张，[R1-fix]）**：id, workspace_id, template_id, token_hash, name, status(active/disabled), last_used_at, created_at。入站 Hook 的 token 存储——token 存 SHA-256 hash（优于 autopilot_trigger.webhook_token 的明文存储 [已验证 `pkg/db/queries/autopilot.sql:127-137` 明文等值查询]，非"镜像"）[R2-fix 措辞]；蓝图 §3 未含此表，属执行期补充（见 §7 偏差声明）。**delivery 审计 P0 降级**：不落专门 delivery 表（既有 webhook_delivery 的 autopilot FK 为 NOT NULL 不可复用 [已验证 `093_webhook_deliveries.up.sql:29-30`]），P0 用 last_used_at + 结构化日志；delivery 表随 P2 出站 webhook 一并设计 [R2-fix]
- `workflow_run`：status(**running/paused**/completed/failed/cancelled/waiting_acceptance）[R3-fix：blocked/熔断需 paused，review #2]；template_snapshot JSONB；**intake_issue_id**（FK issue，追踪父 issue）[R3-fix]；source_type + source_id = **外部工作项标识**（Hook payload 携带，见 §4.1）；**UNIQUE(workspace_id, source_type, source_id, template_id)**（Hook 幂等硬约束，蓝图 §8.3）
- `step_instance`：status(pending/active/dispatched/running/passed/failed/blocked/rework/skipped)；**UNIQUE NULLS NOT DISTINCT (run_id, node_key, parent_step_id, attempt)** [R1-fix：PG 中 NULL 互不相等，普通 UNIQUE 在 P0（parent 恒 NULL）失效；仓内先例 `084_task_usage_dashboard_rollup.up.sql:51`]
- `submission`：status 四态（DONE/DONE_WITH_CONCERNS/BLOCKED/NEEDS_CONTEXT）+ gaps JSONB（D-2）+ artifacts JSONB（只存持久引用——PR URL/分支/附件 ID，禁 workdir 相对路径，D-11）+ exit_fields JSONB + idempotency_key；**UNIQUE(step_instance_id)** [R1-fix]；**idempotency_key 唯一索引按 step 作用域：UNIQUE(step_instance_id, idempotency_key) WHERE idempotency_key IS NOT NULL** [R3-fix：全库唯一会跨 workspace/step 冲突，review #7]
- `verdict`：result(pass/fail/blocked) + root_cause + confidence + evidence JSONB + verdict_by(system/agent/human)；UNIQUE(submission_id)
- `acceptance`：status(pending/approved/rejected) + **step_instance_id（FK，绑定所属 acceptance 节点的 step——区分中途 Spec Freeze 与最终验收）** [R3-fix：review #5] + reviewer_id + decided_at + reject_reason + reject_to_node_key + rework_context JSONB；UNIQUE(step_instance_id) 部分索引 WHERE status='pending'（每个 acceptance step 至多一个待决验收）
- `step_transition`：from_status/to_status/attempt/trigger_by/payload；(step_instance_id, from_status, to_status, attempt) 去重

命名纪律（清单 1.14）：`verdict.result` 只用 pass/fail/blocked；业务审查意见字段一律叫 `decision`，不得出现在 verdict 表。

**协议演进规则（D-9，[R1-fix]）**：所有 JSONB 字段（exit_fields/artifacts/evidence/template_snapshot/run.context）与准出 schema 遵守 forward-compat——①missing ≠ failed（缺字段按 unknown 处理，不当失败）；②未知字段容忍透传，不拒绝不丢弃；③breaking change 升 schema 版本号。准出校验按"缺必填=结构化错误、未知字段=容忍透传"实现。

## 2. 新增文件清单（fork 边界内）

### 后端（Go）

| 文件 | 职责 |
|---|---|
| `server/internal/workflow/engine.go` | StartRun / SignalVerdict / EvaluateEdges / RequestRework（TS-1）；状态机与推进；**最小熔断**（见 §4） |
| `server/internal/workflow/activate.go` | 节点激活：创建 step_instance（只预创建下一节点，D-3）→ 创建节点子 issue → `EnqueueTaskForIssueWithHandoff`（见 §4 上下文注入） |
| `server/internal/workflow/rework.go` | 定向返工：目标节点新 attempt + rework_context 组装（step_transition + verdict 历史）+ 注入消毒 + **下游全部非 skipped step 置 skipped（含已 passed，§4.4）** |
| `server/internal/workflow/template.go` | 模板 CRUD + publish（快照冻结 + agent_selector 固化 UUID，D-7） |
| `server/internal/workflow/seed.go` | standard + bugfix（D-12）种子模板。**standard 节点清单（9 节点，D-1 + 清单 1.4）** [R2+R3-fix]：①规划（agent/executor）→ ②规划门禁（agent/evaluator）→ ③Spec Freeze（acceptance，中途人工关卡）→ ④实现（agent/executor）→ ⑤基线门禁（agent/evaluator）→ ⑥API 门禁（agent/evaluator）→ ⑦审查（agent/evaluator）→ ⑧验收（acceptance）→ ⑨结束（end）。**bugfix 节点清单（6 节点）**：①规划-精简（agent/executor）→ ②实现（agent/executor）→ ③基线门禁（agent/evaluator）→ ④审查（agent/evaluator）→ ⑤验收（acceptance，默认人工，auto_pass 默认关）→ ⑥结束（end） |
| `server/internal/handler/workflow_template.go` | 模板 API |
| `server/internal/handler/workflow_run.go` | Run API + acceptance 决策 API |
| `server/internal/handler/workflow_submission.go` | submission/verdict API（mat_ token + 角色鉴权，见 §4 verdict actor 模型） |
| `server/internal/handler/workflow_hook.go` | 入站 Hook（幂等 + 限流，镜像 `handler/autopilot_webhook.go:305-342` 的限流模式；delivery 审计 P0 降级为 last_used_at + 结构化日志，见 §1） |
| `server/pkg/db/queries/workflow_*.sql` | sqlc 查询（按实体分文件） |
| `server/cmd/server/router_workflow.go` | 路由注册（package main 新文件） |
| `server/cmd/server/workflow_listeners.go` | 事件监听定义（task 事件 → 引擎入口；package main 新文件） |

### 触点（上游文件，≤3 行/文件，共 3 个）[R2-fix 计数]

- `server/cmd/server/router.go`：+1 行 `registerWorkflowRoutes(...)`
- `server/cmd/server/main.go`：+2 行（引擎实例化 + `registerWorkflowListeners(...)`，与 registerAutopilotListeners 同位 [R1-fix：注册调用都在 main.go，listeners.go 是定义文件拿不到引擎实例]）
- `packages/core/types/events.ts`：+2 行（仅新增 2 个 union 成员 `workflow:run-updated` / `workflow:step-updated`；payload 类型放 fork 新文件 `packages/core/types/workflow-events.ts`，不占触点预算）[R2-fix]

### 前端

| 文件 | 职责 |
|---|---|
| `apps/web/app/[workspaceSlug]/(dashboard)/workflows/` [R1-fix：真实 App Router 路径；`apps/web/platform/` 只有 navigation.tsx] | 模板列表/详情（表单编辑）、Run 列表/详情 |
| `apps/desktop` 路由 wiring [R3-fix：CLAUDE.md Web/Desktop Features 要求双端 wiring，review #13] | desktop 端路由接入同一 views |
| `packages/views/workflows/` | TemplateForm / RunDetail / StepTimeline / AcceptancePanel |
| `packages/core/` | API client + React Query hooks + **事件类型扩展**（`workflow:run-updated` / `workflow:step-updated`；后端经现有 SubscribeAll 机制自动广播 workspace 事件 [已验证 `cmd/server/listeners.go:151-193`]，WS 事件更新 React Query 缓存）+ **zod schema（parseWithFallback，workflow API 响应一律过 schema，附 malformed-response 测试——CLAUDE.md API Compatibility [R3-fix]）** |

### CLI（`server/cmd/multica/`）

- `cmd_submission.go`：`multica submission create --task <id> --status <四态> --exit-fields <json> --artifacts <json> --idempotency-key <k>`。**submission 只写提交（含 agent 自声明 status），不写 verdict 实体** [R1-fix]
- `cmd_verdict.go`：`multica verdict create --task <id> --result <pass|fail|blocked> --root-cause <s> --confidence <l>`（**仅 evaluator 角色 step 的 token 可用**，见 §4）+ `multica verdict get --task <id>`
- `cmd_step.go`：`multica step context --task <id>`（节点 instructions + 上游 exit_fields + 本节点 exit_fields schema）

## 3. API 面（扁平路由 + 既有 workspace 中间件 [R1-fix]）

仓内约定：业务资源用扁平路由 + `X-Workspace-Slug` header 解析 [已验证 `cmd/server/router.go:983,1039` + `middleware/workspace.go:47-95`]；不采用 `/api/workspaces/{slug}/` 嵌套。

```
POST   /api/workflow-templates                      创建模板
GET    /api/workflow-templates                      列表
GET    /api/workflow-templates/{id}                 详情
PUT    /api/workflow-templates/{id}                 编辑（draft）
POST   /api/workflow-templates/{id}/publish         发布（快照冻结 + selector 固化 UUID）
POST   /api/workflow-templates/{id}/archive         归档
POST   /api/workflow-hooks                          创建 Hook（签发 token）
GET    /api/workflow-hooks                          列表/禁用
POST   /api/hooks/workflow/{token}                  入站 Hook（幂等 + 限流 + 审计）
GET    /api/workflow-runs                           Run 列表
GET    /api/workflow-runs/{id}                      Run 详情（steps/submissions/verdicts/transitions）
POST   /api/workflow-runs/{id}/acceptance/approve   验收通过
POST   /api/workflow-runs/{id}/acceptance/reject    驳回（reject_to_node_key + reason）
POST   /api/tasks/{id}/submission                   Agent 提交（mat_ token；双层校验）
POST   /api/tasks/{id}/verdict                      evaluator 写 verdict（mat_ token + 角色鉴权）
GET    /api/tasks/{id}/verdict                      查 verdict（mat_ token）
GET    /api/tasks/{id}/step-context                 节点上下文（mat_ token）
```

Hook payload schema：`{title, description, source_id, reviewer, source_url, template_key}`——`source_id` 为外部工作项 ID（幂等键，必填）[R3-fix]；`reviewer` 接受 member id 或 email，解析失败 400 [R1-fix]；解析结果落 acceptance.reviewer_id。重复推送（同 source_type+source_id+template_id）返回 200 + 已有 run_id，不重复创建。

## 4. 引擎核心设计（P0 线性）

### 4.1 Issue 模型与激活序列 [R1+R2+R3 三轮审查修订]

**intake 父 issue + 每节点子 issue**。理由：`EnqueueTaskForIssue` 从 `issue.AssigneeID` 派生 agent [已验证 `service/task.go:651`]，共享单 issue 须逐节点改派，会触发 assign 自动入队造成双任务；每节点独立子 issue 则派发自然（与 harness parent/child 模型同构）。

**source_id 语义（Hook 幂等的前提）** [R3-fix：review #1]：`workflow_run.source_id` = **外部工作项标识**（Hook payload 的 `source_id`，如外部需求系统的需求 ID；manual 触发时为触发 issue id；autopilot 触发时为 autopilot_run id）。intake 父 issue 存 **`workflow_run.intake_issue_id`** 列（§1 已加）。两者不可混用——否则每次重推产生新 UUID，幂等 UNIQUE 永不命中。

**激活序列（顺序硬约束，防双重入队）** [R2-fix]：
1. 以 `status='backlog'` 创建子 issue——backlog 是唯一能跳过 `maybeEnqueueOnAssign` 自动入队的状态 [已验证 `service/issue.go:391-413`：仅 backlog 跳过]
2. 显式调 `EnqueueTaskForIssueWithHandoff`（此时无 pending task 竞争，唯一索引安全 [已验证 `037_fix_pending_task_unique_index.up.sql:6-8`]）
3. 子 issue 翻 todo **走 service 层状态函数**（事件/活动日志/WS 正常触发）[R3-fix：review #9——裸 sqlc 会绕过 `internal/handler/issue.go:2621` 起的全部副作用]

**title 与 duplicate guard** [R2-fix]：title = `<run 序号>-<node_key>-attempt<N>`——retry/rework 的 attempt 递增保证 title 唯一，绕开按 title 的 active 查重 [已验证 `pkg/db/queries/issue.sql:127-135`]。

**子 issue 生命周期** [R2+R3-fix]：step 终态（passed/failed/skipped）时，引擎经 **service 层**把对应子 issue 置 done/cancelled；retry/rework 新建 attempt 子 issue 前，旧 attempt 子 issue 置 cancelled。child-done 父通知由 service 层正常触发，intake 父 issue assignee 为人类或空时不唤醒 agent，仅系统评论。

- step_instance.issue_id → 节点子 issue（当前 attempt）；run.intake_issue_id → intake 父 issue；run.source_id → 外部工作项 ID

### 4.2 上下文注入（零触点机制）[R1-fix：CRITICAL]

**用 `EnqueueTaskForIssueWithHandoff` 的 handoff_note 承载节点上下文** [已验证 `service/task.go:663`；handoff_note 直达开场 prompt `daemon/prompt.go:36-39`]，零上游触点。handoff_note 内容（消毒后）：节点 instructions + 上游 exit_fields 摘要 + 本节点 exit_fields schema +（返工轮次）rework_context（驳回原因 + 历史 verdict 摘要，D-8 显式注入）。语义借用说明：handoff_note 原为"指派时的自由文本指令"（MUL-3375），与节点上下文语义兼容；完整上下文同时落 step_instance/DB，agent 可用 `multica step context` 查询全量。

**Wave 2 冻结的硬约束** [R2-fix]：①长度上限（handoff_note 为无界 TEXT [已验证 `122_task_handoff_note.up.sql:6`]，但 prompt 注入需控制体量，建议 ≤4KB，超出截断并指引 `multica step context` 查全量）；②多行处理——prompt.go 只对首行加 `> ` 前缀，多行内容会脱出引用块框架，注入前须逐行加前缀或剥离换行为单行；③注入消毒（cairn 规则）。

### 4.3 verdict actor 模型 [R1-fix：MAJOR]

节点 config 增加 `role` 字段：`executor`（默认）/ `evaluator` / `reviewer`（P0 种子模板用）。

- **executor step**：agent 提交 submission（含自声明 status）；**system 派生 verdict**（verdict_by=system，映射：DONE→pass、DONE_WITH_CONCERNS→pass（evidence 记 concerns）、BLOCKED→blocked、NEEDS_CONTEXT→blocked）；executor token 调 verdict 写接口 → 403
- **evaluator step**（种子模板中的门禁/审查阶段，P0 以 agent 节点表达，P1 升级 gate 类型）：独立 agent 审查上游产物后经 `multica verdict create` 写 verdict（verdict_by=agent）。**verdict 挂载契约** [R2-fix + R3-fix]：verdict 有 UNIQUE(submission_id) + FK。evaluator 调 verdict create 时若本 step 尚无 submission，服务端在同一事务自动补建 minimal submission 再挂 verdict——**但补建的 submission 必须过同一套准出校验**（review #10）：节点有必填 exit_fields 时，verdict create 必须同时携带这些字段（缺则 400 结构化错误）；只有节点无必填准出字段时才允许 exit_fields={} 的 minimal submission。语义说明：verdict 挂在 evaluator 自己 step 的 submission 上（对上游产物的裁定落在本 step），与蓝图支柱 5 的关系见 §7 偏差 #6
- 生产/验收分离（蓝图支柱 5 + 清单 1.21/1.23）：模板的门禁/审查阶段 agent_selector 必须 ≠ 上游 executor（publish 时校验）

### 4.4 推进与状态映射

- **Run 初始化**（清单 2.8）：判定模板 → run.context 初始化 → 激活首节点（active）+ 预创建下一节点（pending）
- **激活**：step_instance 创建（状态守卫 INSERT）→ 创建子 issue → EnqueueTaskForIssueWithHandoff；**step→dispatched 挂 task:dispatch 事件** [R1-fix：enqueue 时 task 仅 queued，dispatch 发生在 daemon claim，已验证 `pkg/protocol/events.go:34`]
- **verdict 消费**：事务内重读 step 状态（已终态忽略重复信号）→ pass：置 passed，激活下一节点（edge.condition P0 恒 NULL 取默认边）；fail：retry（attempt+1 ≤ max_attempts）或 rework 或转人工；blocked：step 置 blocked，**Run 置 paused**（§1 已补该状态 [R3-fix]）+ inbox 通知
- **返工的下游失效（定向返工核心语义）** [R3-fix：review #4]：rework 到节点 N 时，**N 之后所有非 skipped 下游 step（passed/running/dispatched/active/pending 全部）置 skipped 并写 step_transition**——不只取消 pending。N 以新 attempt 重跑 pass 后，引擎按模板顺序重新激活 N+1（重新过门禁/审查），最终重新到达验收。AC2 断言"驳回后重新过门禁并再次到达验收"（prd.md AC2 同步修订）。
- **task 事件映射** [R1-fix]：task:failed（daemon 崩溃/掉线）→ step failed，按节点策略 retry 或转人工；task:completed 但无 submission → step blocked + Run paused + inbox 通知（P0 最小映射；完整自愈 sweeper 在 P1）
- **熔断（双计数器）** [R3-fix：review #3]：①门禁/失败循环——同节点自上次 pass 以来连续 rework ≥3（`CountConsecutiveReworksForNode`，已有查询）；②验收驳回循环——同一 run 内 acceptance rejected 总数 ≥3（新查询 `CountRejectionsForRun`）。任一触发 → Run paused + intake issue 转派人类 + inbox 通知。单计数器覆盖不了②（目标节点再次 passed 会重置计数）。

### 4.5 验收与节点类型

**acceptance 与 end 是独立节点类型** [R3-fix：review #5]：acceptance 节点激活时创建 acceptance 行（pending，step_instance_id 绑定本 step）+ Run 置 waiting_acceptance + 通知 reviewer（inbox + Lark/Slack 复用）；approve → step passed，推进下一节点（末位验收则推进到 end 节点）；reject → 定向返工（§4.4）+ Run 回 running。end 节点只做收尾：Run completed + intake issue done + 完成通知。中途 Spec Freeze 与最终验收都是 acceptance 节点，靠 acceptance.step_instance_id 区分（§1）。auto_pass：acceptance 节点 config.auto_pass（默认 false，D-12 裁决）且条件满足（前置全部 verdict=pass）时自动 approved。

### 4.6 状态转移守卫

所有 step/run 状态变更用条件 UPDATE（`SET status=$new WHERE id=$1 AND status=$expected`），0 行受影响即放弃重读；每次转移写 step_transition。

## 5. Feature flag

`workflow_engine`（复用 `internal/featureflags` [已验证 keys.go 模式，支持 workspace 维度]）：flag 关闭时路由 404、listeners 不订阅、UI 入口隐藏。默认关闭，内部 workspace 开启。

## 6. 测试策略

- Go 单测：状态机（推进/retry/rework/熔断/幂等/守卫）、**NULLS NOT DISTINCT 重复插入拒绝** [R1-fix]、submission 校验（缺必填=错误、未知字段=容忍）、acceptance 决策、Hook 幂等、task 事件映射
- sqlc：`make sqlc` 后编译通过 [R1-fix：目标名是 `make sqlc`，已验证 `Makefile:348`]
- 前端：`pnpm typecheck` + 关键组件单测
- 端到端（AC1-AC4）：测试脚本模拟——Hook 创建 Run → **按角色 mock agent**（executor 提交 submission、evaluator 写 verdict）走完 4 节点测试模板 → 验收驳回 → 重提交 → approve → 断言 step_transition 全链可追溯
- 回归（AC6）：flag 关闭跑现有 `make test`

## 7. 与蓝图/清单的偏差声明 [R1-fix + R2-fix：逐项列明]

| # | 偏差 | 理由 |
|---|---|---|
| 1 | 新增第 10 张表 `workflow_hook`（蓝图 §3 为 9 张） | Hook token 存储无落点（R1 审查 MAJOR）；token 存 SHA-256 hash（优于 autopilot 明文） |
| 2 | 最小熔断从 roadmap P1-5 提前到 P0 | 清单 1.19/2.6/3.6 三行均标 P0 必需（R1 审查 MAJOR）；P1-5 补完整 sweeper |
| 3 | 上下文注入用 handoff_note 语义借用（蓝图未指定机制） | BuildPrompt 扩展需 ≥4 个上游触点，超预算（R1 审查 CRITICAL）；handoff_note 零触点且语义兼容 |
| 4 | 种子模板门禁阶段在 P0 映射为 evaluator 角色 agent 节点 | P0 无 gate 节点类型（P1 才有）；保持 7 阶段结构完整 |
| 5 | API 用扁平路由 + X-Workspace-Slug，不用 /api/workspaces/{slug} 嵌套 | 仓内既有约定（R1 审查 MAJOR） |
| 6 | evaluator 的 verdict 挂在自己 step 的 submission 上（蓝图支柱 5 表述是"每次 Submission 由系统派生统一裁定"，隐含挂 executor 产物） | verdict 表 UNIQUE(submission_id) + FK 硬约束（R2 审查 MAJOR）；evaluator 裁定是对上游产物的判断，落在自己 step 语义自洽；executor 产物仍由 system 派生 verdict |
| 7 | submission 唯一约束改为 UNIQUE(step_instance_id)（蓝图 §8.2 为 (step_instance_id, attempt)） | step_instance 已是 per-attempt 粒度，原约束冗余且语义含混（R1 审查 MINOR） |
| 8 | 触点从蓝图 §6 的 listeners.go +1 改为 main.go +2 | 注册调用都在 main.go，listeners.go 是定义文件拿不到引擎实例（R1 审查 MAJOR）；触点总数不变 |
| 9 | step_instance 状态枚举去掉 waiting_gate（蓝图 §3 含） | P0 无 gate 节点，无 waiting_gate 场景；P1 加回 |
| 10 | workflow_run 幂等键用 template_id（蓝图 §8.3 写 template_key） | template_id 是 FK 精确引用；template_key 在模板多版本下语义含混（R2 审查 MINOR） |
| 11 | Hook delivery 审计 P0 降级为 last_used_at + 结构化日志 | 既有 webhook_delivery 的 autopilot FK 为 NOT NULL 不可复用（R2 审查 MAJOR）；delivery 表随 P2 出站 webhook 设计 |
| 12 | workflow_run 增加 intake_issue_id 列 + source_id 定义为外部工作项 ID（蓝图 §3 未细分） | source_id 是 Hook 幂等键，intake issue 每次新建，混用则幂等失效（R3 审查 #1） |
| 13 | workflow_run.status 增加 paused（蓝图 §3 无） | blocked/熔断需要暂停态（R3 审查 #2） |
| 14 | 熔断为双计数器（连续 rework + 验收驳回总数；蓝图/清单为单一连续计数） | 单计数器被"再次 passed"重置，覆盖不了验收驳回循环（R3 审查 #3） |
| 15 | acceptance 与 end 为独立节点类型；acceptance 表加 step_instance_id（蓝图 §3 无此列、种子曾合并"acceptance+end"） | schema type 单值；中途 Spec Freeze 与终验须可区分（R3 审查 #5） |
| 16 | 返工使下游全部非 skipped step 失效（含已 passed），重跑时重新过门禁 | 只取消 pending 会跳过已 passed 的门禁直达验收（R3 审查 #4） |
| 17 | submission 幂等索引按 (step_instance_id, idempotency_key) 作用域（蓝图 §8.2 未定 scope） | 全库唯一跨 workspace/step 冲突（R3 审查 #7） |

其余与蓝图 §3/§8、清单 D-1~D-12 一致，无未声明偏差。
