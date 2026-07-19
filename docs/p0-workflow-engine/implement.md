# P0 执行计划

> 三波推进，每波有 review gate（trellis-check 子智能体）与验证命令。所有工作在 multica 仓（fork）内进行，遵守蓝图 §6 fork 卫生。

## Wave 1 · 地基（R1 → R2 → R3/R4）

- [x] 1.1 `901_workflow_core.up/down.sql`：10 张表 + 索引 + UNIQUE 约束（design.md §1；step_instance 用 UNIQUE NULLS NOT DISTINCT）
- [x] 1.2 `pkg/db/queries/workflow_*.sql` + `make sqlc`（目标名已验证 `Makefile:348`），编译通过
- [x] 1.2b **R3 审查修复（migration + 查询层）**：①run.status CHECK 加 paused；②run 加 intake_issue_id 列；③acceptance 加 step_instance_id 列 + 部分索引改按 step；④submission 幂等索引改 (step_instance_id, idempotency_key)；⑤新查询 CountRejectionsForRun + CountConsecutiveReworksForNode（双计数器熔断）、CreateTemplateVersion（版本创建，max+1）、提交幂等查询按 step——已改 901 并重放 dev DB（\d 验证 paused/intake_issue_id/按 step 索引到位，插入/唯一冲突冒烟通过）；`make sqlc && make build && go vet && 迁移 lint` 全过
- [x] 1.3 `internal/workflow/template.go`：模板 CRUD + publish（快照冻结 + selector 固化 UUID + 门禁阶段 selector ≠ 上游 executor 校验）
- [x] 1.4 `internal/workflow/engine.go` + `activate.go`：StartRun / SignalVerdict / EvaluateEdges / 激活（只预创建下一节点；backlog→handoff 入队→service 层翻 todo）/ 状态守卫 / step_transition 写入 / **双计数器熔断（连续 rework ≥3 或验收驳回 ≥3 → paused+转人工+通知）**
- [x] 1.5 `internal/workflow/rework.go`：定向返工 + rework_context 组装 + 注入消毒 + **下游全部非 skipped step 置 skipped（含已 passed）**
- [x] 1.6 `handler/workflow_submission.go`：submission/verdict API（mat_ 鉴权 + verdict actor 模型：executor 403 / evaluator 可写（补建 submission 也过同一套准出校验）/ system 派生 + 双层准出校验：缺必填=错误、未知字段=容忍透传；router.go 触点恰好 +1 行；`go test ./...` 全量零 FAIL）
- [x] 1.7 Wave 1 单测：26 个边界/并发测试函数（engine_edge_test.go），覆盖率 74.7%→82.8%
- [x] **Gate W1**：PASS（trellis-check 自修 7 处：吞错/死代码/UTF-8 截断/ctx 透传；`make test` 41 包零 FAIL、覆盖率 83.1%、router.go +1 行、models.go 纯生成物）。遗留 2 项设计级发现：①终态 step verdict 写入产生噪声行 → Wave 2 加 409 拒写；②熔断②时 acceptance step 残留 active → P1 sweeper/resume 设计时定

## Wave 2 · 链路（R5 → R6 → R10）

- [x] 2.1 `handler/workflow_hook.go`：入站 Hook（token SHA-256 解析 + 限流 + source_id 必填 + reviewer id/email→member 解析 400 + UNIQUE 幂等重推返回已有 run + last_used_at 审计；测试含 201/200/400 矩阵）
- [x] 2.2 `handler/workflow_run.go` + `workflow_template.go`：模板 API（CRUD/publish/archive）+ Hook 管理 API（token 仅创建时返回一次）+ Run API（list/detail 含 acceptances+snapshot/approve/reject，reviewer=当前 member）；`workflow_submission.go` 补终态 step 写入 409
- [x] 2.3 End 通知 + auto_pass：run 完成/waiting_acceptance/熔断 inbox 通知 + auto_pass（默认关，system 触发）；**Lark/Slack 暂为 inbox-only**（channel 发送需 installation+chat 绑定，无现成工作区级通知助手，P2 再议）
- [x] 2.4 CLI：`cmd_submission.go` / `cmd_verdict.go`（create 403→ExitAuth + get + confidence 映射）/ `cmd_step.go`（human+json 输出，`MULTICA_TASK_ID` 回退）；init() 自注册，main.go CLI 侧零触点
- [x] 2.4b `internal/workflow/task_events.go`（引擎映射：dispatch/failed-retry/failed-escalate/completed-no-submission-blocked）+ `cmd/server/workflow_listeners.go`（薄订阅层）+ main.go **恰好 +2 行**（router.go +1 / main.go +2，预算内）；含 flag-off 全 no-op 测试
- [x] 2.6 Wave 2 单测：listeners 6 项 + task_events 7 项 + CLI 3 文件 + `workflow_e2e_test.go` 全链（hook→run→submission→verdict→approve→completed + 幂等重推）；覆盖率 81.7%
- [x] 2.5 节点激活序列（已在 1.4 落地）：backlog 创建 → `EnqueueTaskForIssueWithHandoff`（handoff_note：instructions + 上游 exit_fields 摘要 + schema + rework_context；≤4KB、逐行前缀、消毒）→ service 层翻 todo（`internal/service/issue_status.go`）；title 带 attempt 后缀；子 issue 生命周期经 service 层
- [x] **Gate W2**：PASS（trellis-check 自修 4 处：**CRITICAL 鉴权缺口——mat_ token 可过 RequireWorkspaceMember 调人类 API，executor 可自批验收，已挂 RequireHumanActor + 路由级回归测试**；AC1 通知缺口——Hook run 无 initiator 时通知丢弃，已加 responsibleHuman 回退至 hook reviewer；CLI --output 校验；`make test -race` 41 包零 FAIL、覆盖率 82.1%）

## Wave 3 · 可见（R7 → R8 → AC 验证 + R9）

- [ ] 3.1 `packages/core` API client（**zod parseWithFallback + malformed-response 测试**，CLAUDE.md API Compatibility）+ hooks + 事件类型扩展（workflow:run-updated / workflow:step-updated）；`packages/views/workflows/`（TemplateForm / RunDetail / StepTimeline / AcceptancePanel）
- [ ] 3.2 `apps/web/app/[workspaceSlug]/(dashboard)/workflows/` 路由段接入（NavigationAdapter 遵守包边界）+ **`apps/desktop` 路由 wiring（CLAUDE.md Web/Desktop Features）**；`pnpm typecheck` 通过
- [ ] 3.3 `internal/workflow/seed.go`：standard（9 节点，含 Spec Freeze acceptance + 独立 end）+ bugfix（6 节点）种子模板
- [ ] 3.4 端到端验证脚本：AC1（Hook→4 节点测试模板→通知，**按角色 mock agent**：executor 提交 submission、evaluator 写 verdict）、AC2（驳回→下游已 passed 节点置 skipped 并重跑→再次到达验收）、AC3（缺准出字段→结构化拒绝）、AC4（全链追溯）、AC7（双种子模板实例化+各跑通一次）、AC8（Hook 幂等重推 + reviewer 400）、AC9（CLI 403/可写/step context）
- [ ] 3.5 feature flag 接线 + flag 关闭回归（AC6）：`make test` 全量
- [ ] 3.6 上游合并演练一次（冲突 ≤ 触点预算）+ `make check` 全量（AC5）
- [ ] 3.7 **规划文档入仓（All In Code）**：prd.md / design.md / implement.md + 蓝图与机制清单链接拷入 `multica/docs/p0-workflow-engine/`，随 feat/workflow-p0 分支交付
- [ ] **Gate W3**：`go test -cover ./internal/workflow/...` ≥80% + trellis-check 全 scope + AC1-AC9 逐条证据

## 验证命令

```bash
cd multica && make sqlc && make build            # 编译（目标名已验证 Makefile:348,316）
cd multica && make test                          # Go 测试
cd multica && pnpm typecheck && pnpm test        # 前端
cd multica && make check                         # 全量（Wave 3 必跑）
```

## 回滚点

- 每 Wave 结束为一个回滚点；feature flag 默认关闭，线上随时可关
- migration 901 有 down 文件；触点文件（router/main/events.ts）改动单行可逆

## 风险与注意

- 引擎并发五类竞态（蓝图 §8.1）是测试难点，Wave 1 单测必须覆盖
- 上下文注入走 handoff_note 零触点机制（design.md §4.2），**禁止改上游 daemon/prompt.go**；handoff_note 长度与消毒规则在 Wave 2 冻结
- 子智能体派发时提示词以 `Active task: .trellis/tasks/07-18-p0-standard-requirement-e2e` 开头，并附 implement.jsonl 上下文
