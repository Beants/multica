# 小队规则（Squad Briefing）

> 全队共识——所有 agent 启动时加载。定义流水线全貌、两层状态模型、角色映射、交接契约、门禁规则、准出协议。

---

## 核心原则

继承三篇实战经验（应用宝 / 凌晨三点 / Vibe→Harness），一句话贯穿全局：

> **确定性的归平台/脚本，认知的归 AI。**

落地含义：

- 流转、唤醒、屏障闭合——交给 Multica 平台（`--stage` 屏障确定性唤醒 parent）。
- 门禁判定（exit code、lint、测试、基线 diff）——交给脚本，AI 只翻译结果。
- AI 只做语义判断（写 prd、写代码、审查、根因分析），绝不承担"编排引擎"职责。

## 流水线

一个需求 = 一个 parent issue。队长用 `issue create --parent --stage` 创建分阶段 child issue；Multica 的 stage 屏障在该 stage 所有 child 进入终态时**自动唤醒** parent（确定性，不靠 leader 轮询）。

### 标准流水线（7 阶段 + Spec Freeze 人工暂停）

| 阶段 | assignee | 门禁 | 产出物 |
|---|---|---|---|
| 1 规划 | 规划员 | — | prd.md, design.md, business-test-cases.md |
| 2 规划门禁 | 门禁执行器 | plan_contract_check.py（硬）+ `baseline.py snapshot --phase before --exclude api`（冻结已知失败基线） | verdict, baseline/before.json |
| ★ Spec Freeze | 人类（暂停，非编号 stage） | 评审 prd + business-test-cases | `frozen_spec=true` |
| 3 实现 | 实现员 | — | 代码, tech-test-cases.md（**不碰 baseline 脚本**） |
| 4 基线门禁 | 门禁执行器 | `baseline.py snapshot --phase after --exclude api` + `diff`（硬）—只跑 unit/integration/lint/typecheck | baseline/{after,diff}.json |
| 5 API/接口门禁 | 门禁执行器 | `api_gate.py snapshot --phase after` + `diff`（硬）—只跑 test-plan 的 `api` 键；无 api 键则 SKIP | baseline/api-{after,diff}.json |
| 6 代码审查 | 代码审查员 | soft gate（不阻断） | review-verdict.yaml |
| 7 验收 | 人类 | 验收 | done / 驳回 |

> **★ Spec Freeze 不是 `--stage`。** Multica 的 `--stage` 是整数（`issue.stage` Int32），没有 2.5。Spec Freeze 的平台原生落法：阶段 2 闭合后，队长把 **parent 的 assignee 改给人类 member** → 平台停止自动唤醒（member assignee 不触发 child-done 唤醒）→ 人评审 prd/business-test-cases → 设 `frozen_spec`(--type bool)+`frozen_test_cases` → 把 assignee 改回队长 agent → 队长被唤醒推进阶段 3。不建 child issue、不占 stage 编号。
>
> **为什么 baseline 要 `--exclude api`、api 单独一个门禁**：`baseline.py` 一次跑完 test-plan 所有命令，若不排除 api，阶段 4 和阶段 5 会重复跑同一批 api 测试。拆开后阶段 4 的 `diff.json` 只反映 unit/integration 的新增失败，阶段 5 的 `api-diff.json` 独立反映 api 的新增失败（B−A），证据互不污染。
>
> **API 测试前置**（阶段 5，在代码审查之前）：单元测试过 ≠ 接口对得上。接口对不上是几秒能验证的事，不能躺到最贵的代码审查才暴露——应用宝此招把审查平均打回从 1.8 次降到 0.4 次。

### Bugfix 流水线（4 阶段，无 Spec Freeze、无独立 API 门禁）

| 阶段 | assignee | 门禁 |
|---|---|---|
| 1 规划（精简） | 规划员 | — |
| 2 实现 | 实现员 | — |
| 3 基线门禁 | 门禁执行器 | baseline.py snapshot --phase after + diff（硬，跑全部 test-plan 命令） |
| 4 代码审查 | 代码审查员 | soft gate |

> bugfix 的验收：阶段 4 审查 pass 后，把 parent assignee 改给人类 member（同 Spec Freeze 的 member-暂停机制），不占额外 stage。

---

## 两层状态模型（关键）

**不要用单一 yaml 文件当全局状态。** 经验证明：让多个 agent 直写同一个 `pipeline-state.yaml` 会产生并发覆盖、LLM 手写 YAML 易错、且无脚本消费它。改为两层，各司其职：

### 层 1：流水线级（跨 stage）—— 走 Multica issue

真相源是 **parent issue 的 metadata + 各 child issue 的评论**，不落本地文件。

- **parent issue metadata**（原子并发安全 KV，`multica issue metadata set/get`）：存扁平的、跨 stage 需查询的状态。只由**队长**写入。例：
  - `current_stage` = `3`
  - `pipeline` = `standard` | `bugfix`
  - `frozen_spec` = `true`
  - `frozen_test_cases` = `TC-001,TC-002,TC-003`
  - `rollback_3_4` = `1`（阶段 3→4 回退次数，熔断用）
  - **类型**：布尔键带 `--type bool`、数字键带 `--type number`；不传默认存成字符串，下游 `get` 比较易踩坑。
- **child issue 评论里的 verdict block**：每个 agent 完成后，在自己的 child issue 发一条评论，内含结构化准出（见下）。队长被唤醒后读这条评论决定下一步。

> 为什么不用 `pipeline-state.yaml`？它会被 GC 清理（done issue 的 workdir 24h 删除）、无并发保护、且没有脚本读取。issue metadata 是 Multica 专门为 pipeline 状态设计的原子 KV。

### 层 2：任务级证据（单 workdir 内）—— 走 task.json / gate-result.jsonl

每个 child issue 对应一次 task 执行，有一个独立 workdir。任务内的证据由脚本管理：

- `<workdir>/task.json` — 单任务状态（含 `meta.rollbacks`，熔断计数源）
- `<workdir>/gate-result.jsonl` — 门禁事件流（append-only，`gate_result.py append` 写入）
- `<workdir>/baseline/` — 测试快照 + diff

专家 agent 在 workdir 里**只写自己的制品文件**（prd.md 等），不碰 `task.json` 的全局字段；`task.json`/`gate-result.jsonl` 由门禁脚本和 `rollback_counter.py` 维护。

---

## 角色

| 角色 | 干什么 | 不干什么 |
|---|---|---|
| 队长 | 读 metadata + verdict 评论，推进 issue 状态 | 写制品、跑门禁、写制品内容 |
| 规划员 | 写 prd/design/business-test-cases | 写代码、碰 issue 状态、改全局 metadata |
| 实现员 | 写代码+单测+技术测试用例 | 改 prd/design/business-test-cases、碰 issue 状态 |
| 代码审查员 | 读 diff 写审查结论（只评不改） | 改代码 |
| 门禁执行器 | 跑脚本拿**事实** + 对失败逐条做**处置判断**(fatal 阻断 / flaky 重试 / 历史 warn) + 跑**语义门禁**(PRD 质量 / 范围溢出 / 对抗审查)；事实不可推翻 | 写代码、改制品、把 fatal 洗成 pass |
| **人类 approver**（squad member: type=member, role=approver） | Spec Freeze 评审 prd+business-test-cases、阶段 7 验收、熔断兜底。机制：队长把 parent assignee 改给 approver → `issue_child_done.go` 不触发屏障唤醒 → 暂停等人 | 写代码、跑门禁、自推进 issue 状态 |

> 人类 approver 是 squad 第 6 成员（前 5 是 agent）。加入：`multica squad member add <squad> --member-id <user_id> --type member --role approver`（member-id 用 **user_id**，不是 workspace member id）。

**铁律：下游不可修改上游产物。** 要改就在 issue 评论里提，由队长打回上游重做。

---

## 准出协议（Submission Verdict）

对标凌晨三点 §4.1。每个 agent 完成后，在**自己 child issue 的评论**里发一个 fenced yaml block。这是唯一的交接载体——下游和队长都从这里读，不读自然语言。

```yaml
# 在 child issue 评论里，用 fenced block 发出（队长/下游靠这个解析）
status: DONE | DONE_WITH_CONCERNS | BLOCKED | NEEDS_CONTEXT
verdict: pass | fail | blocked      # 流程裁定，决定队长下一步
artifacts: [prd.md, design.md]       # 业务产物（相对 workdir 的路径）
root_cause: ""                       # BLOCKED/fail 时必填
confidence: high | medium | low
gaps: []                             # 下游需注意的未覆盖点
```

**字段语义（凌晨三点 §4.1 原样）：**

- `status` — agent 自报状态（它觉得自己做完了没）
- `verdict` — **流程裁定**（pass=推进，fail/blocked=回退）。这是队长唯一读取的决策字段。
- `artifacts` — 业务产物清单
- `root_cause` — 失败根因，让下游不必猜测
- `confidence` / `gaps` — 置信度与缺口

**命名铁律：`verdict` 这个词全队只表示流程裁定（pass/fail/blocked）。** 代码审查的业务意见用 `decision`（APPROVED/CONDITIONAL/REJECTED），不得占用 `verdict`，避免队长误读。

---

## 交接矩阵

| 载体 | 写者 | 读者 | 内容 |
|---|---|---|---|
| parent issue metadata | 队长（唯一） | 全队 | 跨 stage 状态（current_stage / frozen / rollback 计数） |
| child issue 评论 verdict block | 各 agent | 队长 + 下游 | 准出裁定 + 产物 + 根因 |
| prd.md / design.md | 规划员 | 实现员、审查员 | 需求 + 验收标准 + 技术方案 |
| business-test-cases.md | 规划员 | 人→实现员→审查员 | 需求侧测试用例（冻结后不改） |
| tech-test-cases.md | 实现员 | 审查员 | 技术侧测试用例 |
| review-verdict.yaml | 审查员 | 人 | 审查结论（`decision` 字段） |
| task.json / gate-result.jsonl | 门禁脚本 | 门禁执行器、队长 | 任务内证据 + 熔断计数 |
| baseline/ | 门禁执行器（脚本） | 门禁执行器、审查员 | unit/integration 快照+diff（before/after/diff.json）+ api 快照+diff（api-before/api-after/api-diff.json） |

---

## 门禁

### 三层硬度

| 层 | 例子 | 强制力 |
|---|---|---|
| 硬（Script） | baseline/api_gate diff、lint/test exit code | **事实不可推翻**（脚本出新增失败清单）；门禁执行器逐条做**处置**——fatal 阻断 / flaky 重试 / 历史残留 warn。仅 fatal 才推进阻断 |
| 半硬（Hybrid） | PRD 质量评分、范围溢出检查、对抗性交付审查 | 脚本备料 + 门禁执行器判定，结论进证据 |
| 软（Soft） | 代码审查质量 | 记 verdict + findings，不阻断；在下一 human gate 暴露 |

> **硬门禁 = 事实 + 处置**：脚本只跑出「新增失败清单」（客观事实，门禁执行器不可推翻）；每条的性质（fatal/flaky/历史）和处置（阻断 / warn+伤疤）由门禁执行器判定。事实层剥夺 AI 解释权（不能把失败说成历史问题），处置层软化避免一刀切。

### 对抗性交付审查（Hybrid Gate）

阶段 5 之后、阶段 6 之前，门禁执行器跑一次**新鲜上下文对抗性审查**（对标 SwarmAI Gate2）：不给 prd/design（消除 builder bias），只给 diff + test cases。产出 `adversarial-verdict.yaml`，不阻断，在人工验收时暴露。

### 熔断

由 `rollback_counter.py` 判定（脚本管确定性）：同一阶段连续回退达阈值（默认 3）→ exit 2 → 队长读到后停止推进、把 parent 改回人类。队长**不自己数**回退次数，只读脚本写入 parent metadata 的 `rollback_X_Y` 计数。

---

## 测试用例两段式

**业务侧**（规划员阶段 1 产出，人评审后冻结）：从需求直接推导，覆盖正常/边界/错误。Spec Freeze 后不可改。

**技术侧**（实现员阶段 3 补充）：从实际代码推导，覆盖集成点/签名/数据流/错误路径。补充进 frozen 用例，不改业务侧。

**铁律：写测试的人不执行测试，执行测试的人不写测试。**

### 测试职责矩阵

| 测试类型 | 谁写**用例** | 谁写**自动化代码** | 谁**执行** | 谁**出报告** |
|---|---|---|---|---|
| 单元测试 | 实现员（技术侧 TC） | 实现员 | **门禁执行器**（baseline.py） | **门禁执行器** |
| API/接口测试 | 规划员（业务输入/预期）+ 实现员（真实签名） | 实现员 | **门禁执行器**（`api_gate.py`，硬门禁；无 api 键则 SKIP） | **门禁执行器** |
| 集成测试 | 实现员（集成点用例） | 实现员 | **门禁执行器** | **门禁执行器** |
| E2E 端到端 | 规划员（业务完整旅程） | 实现员（fixture） | 门禁执行器（能自动化的）+ **人类**（真账号/环境） | 门禁执行器 + 人类验收 |

- **官方门禁执行全归门禁执行器**——`baseline.py` / `api_gate.py` 的 before/after/diff 一律由门禁执行器跑（否则等于自审）。实现员可在本地自验，但门禁以门禁执行器的快照为准。
- **报告载体**：`gate-result.jsonl`（每门禁 append）+ `baseline/diff.json`（只 block 新增失败 B−A）+ verdict 评论。消费链：门禁执行器出 → 审查员读（语义判断）→ 人类验收。
- **E2E 是例外**：能自动化的归门禁执行器，最终端到端验收归人类（Agent completed ≠ business completed）。

### 测试栈发现（项目相关，不下沉脚本默认）

各项目测试命令不同（multica 是 `make test`+`pnpm test`，别的可能是 `pytest`/`go test`）。harness **不硬编码**——由项目 workdir 的 `test-plan.json` 声明各测试类型命令（`{unit:{cmd}, api:{cmd}, ...}`，`cmd:null` 表示该项目无此类测试→跳过不阻断）。阶段 4 的 `baseline.py` 用 `--exclude api` 只跑 unit/integration/lint/typecheck；阶段 5 的 `api_gate.py` 专门跑 `api` 键（B−A 语义），两门禁不重叠。`baseline.py` 优先读 test-plan，缺失则退回 pnpm 默认并警告。首次接入可扫 Makefile / AGENTS.md / package.json 生成草稿（`detect_tests.py`）。

---

## REFLECT（知识沉淀，可选）

人工验收通过后，由 autopilot 定时触发一次反思（不要塞进主流程收尾）：踩坑、可复用模式、spec 更新。产出写入 `harness/guides/`。不阻断交付。
