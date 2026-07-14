# Baseline and Gate-Result Protocol (v0)

> evidence runtime v0 的 schema 参考。产出供 dashboard 包消费的文件。何时使用这些文件见 [Evidence and Quality Gates](./evidence-and-quality-gates.md)。

## 文件位置

位于 `.trellis/tasks/<task>/` 下：

```text
evidence-plan.json              已 review 的基线命令契约或明确 N/A 理由
gate-result.jsonl              append-only 事件日志；每个 gate 事件一行 JSON
baseline/
  before.json                  实现前的失败快照
  after.json                   实现后的失败快照
  diff.json                    计算出的 new/known/resolved 失败集合
```

dashboard 等通用 consumer 读取旧任务时，缺失文件仍显示为 `unknown`，而不是伪装成测试失败。Harness 交付则更严格：`evidence-plan.json` 必需；`required` 模式必须有完整 before/after/diff/pass 证据链，`not_applicable` 模式必须有唯一匹配的 skipped 事件。

## Schema 版本

- v0 下 `schema: 1` 固定不变。
- 前向兼容：consumer 容忍未知字段。
- helper 在重写时从不删除未知字段（按文件类型 append-only 或整体重写）。
- 破坏性变更要 bump `schema`，称为 v1，不再是 v0。

## `evidence-plan.json` 契约

有可执行回归命令时使用 `required`：

```json
{
  "schema": 1,
  "mode": "required",
  "commands": [
    {"id": "test", "command": "python3 -m unittest discover -v"}
  ]
}
```

- `commands` 至少一项；每项 `id` 唯一、非空且在任务周期内保持稳定。
- `command` 非空，并拒绝 `true`、`:`、`echo`、`printf` 等明显空跑命令。
- 命令保留既有 shell 执行语义；plan 校验是防漏项机制，不是权限沙箱。调用者仍须获得运行这些命令所需的授权。

确实没有适用的可执行回归检查时使用 `not_applicable`：

```json
{
  "schema": 1,
  "mode": "not_applicable",
  "reason": "仅修改说明文档，没有可执行回归套件"
}
```

`reason` 必须非空且具体。canonical plan 使用 UTF-8、sorted-key、紧凑 JSON；其 SHA-256 写入 plan 驱动的 before/after snapshot。激活后修改 plan 会导致交付失败，即使命令字符串碰巧相同。

## `gate-result.jsonl` 事件 Schema

每个 gate 事件一个 JSON 对象：

```json
{
  "schema": 1,
  "ts": "2026-07-08T10:23:00Z",
  "task": "07-08-evidence-runtime-v0",
  "phase": "plan|implement|check|finish",
  "gate": "prd|design|baseline|lint|typecheck|test|self-review|soft-gate",
  "command": "pnpm test",
  "status": "pass|fail|warn|skipped",
  "duration_ms": 0,
  "hard": true,
  "summary": "short human-readable result",
  "evidence": ["relative/path/or/source-id"],
  "new_failures": 0
}
```

### 字段规则

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `schema` | int | yes | v0 下恒为 `1`。 |
| `ts` | string | yes | ISO 8601 UTC。 |
| `task` | string | yes | task 目录名（如 `07-08-evidence-runtime-v0`）。 |
| `phase` | enum | yes | `plan\|implement\|check\|finish` 之一。 |
| `gate` | enum | yes | 见下方 Gate Enum 表。 |
| `command` | string | yes | 实际运行的 shell 命令。非命令型 gate（如 `self-review`）允许空字符串。 |
| `status` | enum | yes | `pass\|fail\|warn\|skipped`。 |
| `duration_ms` | int | yes | 挂钟耗时。非命令型 gate 为 `0`。 |
| `hard` | bool | yes | `true` = 硬门禁（可阻断）；`false` = 软门禁（仅留疤）。 |
| `summary` | string | yes | 单行结果。`fail` 时必须包含失败指示（如 exit code、第一条错误）。 |
| `evidence` | string[] | yes | 相对路径或 source ID。允许空数组。 |
| `new_failures` | int | yes | 来自 baseline diff 的计数。非 baseline gate 为 `0`。 |

### Gate Enum

| Gate | 默认 `hard` | 典型 `phase` |
|---|---|---|
| `prd` | false | plan |
| `design` | false | plan |
| `baseline` | true | implement (before), check (after) |
| `lint` | true | check |
| `typecheck` | true | check |
| `test` | true | check |
| `self-review` | true | plan |
| `soft-gate` | false | any |

## Baseline 文件 Schema

### `baseline/before.json` 与 `baseline/after.json`

结构相同。由 `baseline.py snapshot` 产出：

```json
{
  "schema": 1,
  "task": "07-08-evidence-runtime-v0",
  "ts": "2026-07-08T10:00:00Z",
  "plan_sha256": "<canonical evidence-plan SHA-256>",
  "evidence_plan": {"sha256": "<canonical evidence-plan SHA-256>"},
  "commands": {
    "lint": {"command": "pnpm lint", "exit_code": 0, "failures": []},
    "typecheck": {"command": "pnpm typecheck", "exit_code": 1, "failures": ["src/foo.ts:12:7 - error TS2322"]},
    "test": {"command": "pnpm test", "exit_code": 1, "failures": ["FAIL src/foo.test.ts > bar"]}
  }
}
```

`commands` 是以字符串为键的 map。旧 `--commands` 路径默认键为 `lint`、`typecheck`、`test`；`--plan` 路径使用 plan 中的稳定命令 ID。`failures` 是归一化失败签名的字符串数组。

plan 驱动的 snapshot 必须写入相同的 `plan_sha256`。标准激活入口创建的 before 还会写入：

- `captured_task_status: "planning"`：证明快照先于状态翻转。
- `workspace_sha256`：Git HEAD、tracked binary diff 与未跟踪文件内容的指纹；仅排除当前任务的 `baseline/` 和 `gate-result.jsonl`。

这两个字段用于安全重试。只有任务仍为 `planning`、plan SHA 和 workspace SHA 都一致时，激活入口才复用已有 before；它绝不覆盖 before。

### `baseline/diff.json`

由 `baseline.py diff` 产出：

```json
{
  "schema": 1,
  "task": "07-08-evidence-runtime-v0",
  "before": "baseline/before.json",
  "after": "baseline/after.json",
  "new_failures": ["src/bar.ts:5:3 - error TS2345"],
  "known_failures": ["src/foo.ts:12:7 - error TS2322"],
  "resolved_failures": [],
  "blocking": true
}
```

`blocking = (new_failures.length > 0)`。是否阻断由调用方（gate_result.py 或 AI）决定；该文件只是证据。

## 失败归一化（best-effort）

失败签名按命令名归一化。未知格式则记录匹配 `/error|fail|✗/i` 的行的前 200 字符。

| 格式 | 正则 |
|---|---|
| TypeScript | `^(?<file>.+?):(?<line>\d+):(?<col>\d+)\s*-\s*error\s+(?<code>TS\d+)\s+(?<msg>.+)$` |
| ESLint | `^(?<file>.+?):(?<line>\d+):(?<col>\d+):\s+(?<msg>.+?)\s+\[` |
| vitest / jest | `^\s*FAIL\s+(?<file>.+?)\s+>\s+(?<name>.+)$` 或 `✕ <name> (<time>)` |
| 未知 | 匹配 `/error\|fail\|✗/i` 的行的前 200 字符 |

dashboard 必须容忍自由格式的字符串。

## Scenario: Evidence-first Task Activation

### 1. Scope / Trigger

当 Harness 任务从 `planning` 进入 `in_progress` 时适用。目标是保证 before 证据先于状态翻转，并让绕过标准入口的任务无法静默交付。

### 2. Signatures

```text
evidence_activate.py --task <task-name-or-path>
baseline.py snapshot --task <task> --phase after --plan <plan-path>
delivery_checklist.py --task <task>
```

### 3. Contracts

- 输入：任务目录必须有合法 `task.json` 和 `evidence-plan.json`。
- required 输出：planning 状态的 before、同 plan 的 after、diff、hard baseline pass。
- not_applicable 输出：唯一匹配 plan reason 的 soft skipped baseline 事件。
- 边界：包装器只委托项目本地 `task.py start`，不修改 Trellis CLI、项目 config 或平台权限。

### 4. Validation & Error Matrix

| 条件 | 结果 |
|---|---|
| task 不是 `planning` | exit 2；不采集、不委托 start |
| plan 缺失、JSON 损坏或违反 schema | exit 2；不委托 start |
| required 首次激活 | 先写 before 和指纹，再委托 start |
| required 重试且 plan/workspace 指纹一致 | 复用 before；绝不覆盖 |
| required 重试且任一指纹变化 | exit 2；不委托 start |
| not_applicable 重试且恰好一条事件完全匹配 | 复用事件，再委托 start |
| not_applicable 事件重复、损坏或 reason 不一致 | exit 2；不追加第二条事件 |
| 委托的 `task.py start` 失败 | 原样返回其 exit code；已采集证据保留供合法重试 |
| in_progress 任务缺 before | 激活入口拒绝回填；delivery checklist 阻断；人工例外也不转成 Harness PASS |

### 5. Good / Base / Bad Cases

- Good：required plan 在激活前 review，before/after 命令与 plan SHA 一致，diff 无新失败且 hard gate pass。
- Base：纯文档任务使用具体 reason 的 not_applicable plan，并保留唯一 skipped 事件。
- Bad：直接运行原始 `task.py start` 后再伪造 before，或通过修改 plan 避开失败。

### 6. Tests Required

- 断言 before 文件在委托 start 观察任务状态前已经存在。
- 断言 required 重试只在 plan/workspace 指纹一致时复用 before。
- 断言 not_applicable 重试保持唯一事件，reason 改变时拒绝。
- 断言 delivery 阻断缺失 plan、命令不一致、plan SHA 不一致、缺 hard pass 和 blocking diff。
- 断言 manifest 安装两个 evidence runtime 文件，且 activation 入口可执行。

### 7. Wrong vs Correct

Wrong：

```bash
python3 ./.trellis/scripts/task.py start <task-dir>
```

Correct：

```bash
python3 ./.trellis/workflows/ai-native-harness-dev/scripts/evidence_activate.py \
  --task <task-dir>
```

## Helper 命令契约

### `.trellis/workflows/ai-native-harness-dev/scripts/evidence_activate.py`

```text
--task <task-dir-name-or-path>
```

标准激活入口只接受 `planning` 任务。它先读取任务目录中的 `evidence-plan.json`：

- `required`：在项目根目录执行 plan 命令，写入带 plan/workspace 指纹的 `baseline/before.json`，再用当前 Python 解释器和参数数组委托 `.trellis/scripts/task.py start`。
- `not_applicable`：先追加一条 `gate=baseline`、`status=skipped`、`hard=false`、`command=""`、`summary=<reason>`、`evidence=["evidence-plan.json"]` 的事件，再委托 start。

委托失败后可以在任务仍为 planning 时重试。required 的 before 只在 plan/workspace 指纹完全一致时复用；not_applicable 只在 reason 未变化且恰好已有一条匹配事件时复用，重复、损坏或不一致的 activation 事件都会拒绝。任务已是 `in_progress` 时，入口拒绝制造或回填 before。直接调用低层 `task.py start` 无法由 marketplace 拦截，但缺失或不一致的证据会被 delivery checklist 阻断。

### `.trellis/workflows/ai-native-harness-dev/scripts/gate_result.py append`

```
append --task <task-dir-name>
       --phase plan|implement|check|finish
       --gate prd|design|baseline|lint|typecheck|test|self-review|soft-gate
       --status pass|fail|warn|skipped
       --command "<shell command>"
       --duration-ms <int>
       --hard|--soft
       --summary "<one line>"
       [--evidence path1,path2,...]
       [--new-failures <int>]
       [--task-root <path>]   # 默认：自动探测 .trellis/tasks/
```

向 `<task-root>/<task>/gate-result.jsonl` 追加一行 JSON。文件不存在则创建。校验 enum 值；非法 enum 以 exit 2 退出。绝不覆盖已有行（append-only）。

### `.trellis/workflows/ai-native-harness-dev/scripts/baseline.py snapshot`

```
snapshot --task <task-dir-name>
         --phase before|after
         [--task-root <path>]
         [--plan <evidence-plan-path> | --commands lint,typecheck,test]
         [--cwd <path>]                      # 默认：项目根目录
         [--if-missing]
```

`--plan` 与 `--commands` 互斥。标准 Harness 路径使用 `--plan`，依次运行 plan 中的命令并写入 canonical plan SHA；旧调用方仍可使用 `--commands`。`not_applicable` plan 不能创建 snapshot。`--if-missing` 只为兼容旧调用方保留，标准 before 由 `evidence_activate.py` 负责，after 由 Phase 2.1b 显式创建。

### `.trellis/workflows/ai-native-harness-dev/scripts/baseline.py diff`

```
diff --task <task-dir-name>
     [--task-root <path>]
```

读取 `baseline/before.json` + `baseline/after.json`，计算 new/known/resolved 失败集合，写入 `baseline/diff.json`，打印摘要。

- Exit 0：非阻断
- Exit 1：阻断（存在 new failures）
- Exit 2：缺少 before.json 或 after.json（附提示信息）

## Canonical 命令序列

```bash
# 1. review <task-dir>/evidence-plan.json 后，在 planning 状态激活
python3 ./.trellis/workflows/ai-native-harness-dev/scripts/evidence_activate.py \
  --task <task-dir>

# 2. 在此处进行实现

# 3. required 模式：实现后使用同一份 plan
python3 ./.trellis/workflows/ai-native-harness-dev/scripts/baseline.py snapshot \
  --task <task-dir> --phase after --plan <task-dir>/evidence-plan.json

# 4. required 模式：计算 diff
python3 ./.trellis/workflows/ai-native-harness-dev/scripts/baseline.py diff \
  --task <task-dir>

# 5. required 模式：记录 hard baseline gate
python3 ./.trellis/workflows/ai-native-harness-dev/scripts/gate_result.py append \
  --task <task-dir> --phase implement --gate baseline \
  --command "evidence-plan.json" --status pass \
  --duration-ms 0 --hard --summary "new_failures=0" \
  --evidence baseline/diff.json --new-failures 0
```

`not_applicable` 模式只执行第 1 步；激活入口写入的唯一匹配 soft skipped 事件就是完整 baseline 证据，不创建 after/diff，也不伪造 hard pass。

交付前，`delivery_checklist.py` 会重新读取当前 plan，并校验 required 的 before/after 命令契约、三方 plan SHA、非阻断 diff 和 hard pass；not_applicable 则要求恰好一条与 reason 完全匹配的 soft skipped 事件。

## 前向兼容

consumer（dashboard、未来的 scoring、未来的平台 hook）必须：

- 把缺失文件当作 `unknown`，而不是 `failed`。
- 容忍未知的 JSON 字段（前向兼容）。
- 重写时绝不静默丢弃未知字段（helper 保留它们）。
- 破坏性变更时 bump `schema`。

## 非目标（v0）

- `.phase-metrics.jsonl`（独立的子任务）
- evaluation scoring 字段（advisory 或 hard）
- 多项目 baseline 聚合
- baseline 轮转 / 清理
- 修改 Trellis CLI 或自动改写项目拥有的 `.trellis/config.yaml`
- 绕过平台命令授权或把激活包装器当作权限沙箱
