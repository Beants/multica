# Dashboard Evidence Contract

> Trellis Dashboard 包消费 Plan 就绪度、证据摘要与 attention 项所用的 TypeScript 接口与 consumer 规则。实现 dashboard scanner 或 UI、或修改 evidence schema 时使用本指南。

## 原则

dashboard 读文件；它不持有状态。每个展示的指标都必须能追溯到 `.trellis/tasks/<task>/` 下的某个磁盘文件。缺失的文件是 `unknown`，不是 `failed`。

## 文件来源

dashboard scanner 按 task 读取这些可选文件：

| 文件 | Producer | dashboard 用途 |
|---|---|---|
| `task.json` | Trellis | 生命周期 status、分支、PR URL |
| `prd.md`、`design.md`、`implement.md` | Code CLI | Task Detail 的 artifact 内容 |
| `plan-state.yaml` | Code CLI | Plan / Spec Freeze status、就绪 flag |
| `source-index.md` | Code CLI | source 覆盖摘要 |
| `plan-self-check.md` | Code CLI | self-check 通过/失败 |
| `open-questions.md` | Code CLI | 未决的人工决策 |
| `decision-log.md` | Code CLI | 已收敛的决策 |
| `acceptance-matrix.md` | Code CLI | 需求到测试的映射 |
| `impact-map.md` | Code CLI | 代码/数据/接口影响 |
| `task-map.md` | Code CLI | 父/子拆解 |
| `gate-result.jsonl` | evidence helper | gate 结果事件 |
| `baseline/before.json` | evidence helper | 实现前快照 |
| `baseline/after.json` | evidence helper | 实现后快照 |
| `baseline/diff.json` | evidence helper | 计算出的 new/known/resolved 失败 |

除 `task.json` 外所有文件都是可选的。当只有 `task.json` + `prd.md` 时 dashboard 也必须能工作。

## 接口

### `PlanReadiness`

由 `plan-state.yaml` + artifact 是否存在计算而来。

```ts
interface PlanReadiness {
  status: string | null;          // plan-state.yaml.status；文件缺失则 null
  sourceIndexReady: boolean;      // source-index.md 存在且 ≥ 1 行非模板行
  prdReady: boolean;              // prd.md 存在且非空
  designReady: boolean;           // design.md 存在（仅复杂 task）
  implementReady: boolean;        // implement.md 存在（仅复杂 task）
  selfCheckPassed: boolean;       // plan-self-check.md status: pass
  specFreezeConfirmed: boolean;   // plan-state.yaml.readiness.spec_freeze_confirmed
}
```

### `EvidenceSummary`

由 `gate-result.jsonl` 聚合而来。

```ts
interface EvidenceSummary {
  total: number;            // 总事件数
  pass: number;
  fail: number;
  warn: number;
  skipped: number;
  hardFailures: number;     // hard: true 且 status: fail 的事件
  softScars: number;        // hard: false 且 status 属于 [warn, fail] 的事件
  latestAt: string | null;  // 最近事件的 ISO 8601；无事件则 null
}
```

### `AttentionItem`

由跨 task 的信号派生。在 Attention Inbox 中按严重度排序。

```ts
interface AttentionItem {
  severity: "critical" | "warning" | "info";
  kind:
    | "stale-task"               // planning > 7d 或 in_progress > 14d
    | "gate-failure"             // 硬门禁 status: fail，未解决
    | "soft-scar"                // 软门禁 warn/fail，已持久化
    | "baseline-regression"      // diff.json blocking: true
    | "dirty-worktree"           // worktree 脏或领先 remote > 24h
    | "missing-plan";            // in_progress 但缺 prd.md，或 completed 但缺 gate-result.jsonl
  projectId: string;
  taskId?: string;
  title: string;                 // 单行人类可读描述
  evidence?: string[];           // 支撑该项的文件路径或 source ID
}
```

### `BaselineDiffSummary`

由 `baseline/diff.json` 计算。文件缺失则为 null。

```ts
interface BaselineDiffSummary {
  blocking: boolean;
  newFailures: string[];      // 归一化失败签名
  knownFailures: string[];
  resolvedFailures: string[];
  beforeAt: string | null;    // before.json 的 ts
  afterAt: string | null;     // after.json 的 ts
}
```

## 严重度排序

Attention Inbox 项的排序：

1. `critical` → `baseline-regression`、`gate-failure`
2. `warning` → `stale-task`、`soft-scar`、`dirty-worktree`、`missing-plan`
3. `info` → advisory 备注（如 spec freshness 候选）

同一严重度内，更近的 `ts` 排在前面。

## 展示规则

### Overview（Attention Inbox）

- Overview 顶部最多 3-5 张卡片。
- 卡片可点击；跳转到 Task Detail。
- 空状态展示一个 CTA（如 "All clear. Start a new task."）——绝不留白屏。
- **首页不放图表。** 图表移到二级页面（依项目约定）。

### Task Detail（Evidence Panel）

- 在 Requirements / Design / Implementation 旁加一个 "Evidence" 标签。
- 最近的 gate 事件置顶；按 phase 分组。
- `fail` 且 `hard: true` 显示红色；`warn` 黄色；`pass` 绿色；`skipped` 灰色。
- 软疤展示但视觉弱化（小图标，不用红色）。
- Baseline diff 摘要展示 "new/known/resolved" 计数；点击展开完整失败列表。

### 就绪 Badge

- 展示在 Task Detail 头部和 Project Detail。
- 把 `PlanReadiness` 映射为 4 个状态：
  - `not_started`（无 artifact）
  - `planning`（有 artifact，self-check 未通过）
  - `ready`（self-check 已通过，等待用户确认 Spec Freeze）
  - `frozen`（Spec Freeze 已确认，可执行 `task.py start`）

## Scanner 容错

- 缺失 `plan-state.yaml` → 除可由 artifact 是否存在计算的字段外，其余 `PlanReadiness` 字段为 false。
- 缺失 `gate-result.jsonl` → `EvidenceSummary.total = 0`，所有计数为 0，`latestAt = null`。
- 缺失 `baseline/diff.json` → `BaselineDiffSummary` 为 null；不要作为 `baseline-regression` 上报。
- 畸形 JSON → 记录 WARN，跳过该文件，继续扫描。不要让整个项目扫描失败。
- 未知 JSON 字段 → 保留；不丢弃。前向兼容见 [Baseline and Gate-Result Protocol](./baseline-and-gate-result-protocol.md)。

## API 契约约定

- API 响应必须包含支撑每个非 null 字段的源文件路径。UI 用这些路径做"跳转到文件"。
- 分页：Attention Inbox 默认 5 项，可展开到 50。Task Detail evidence 默认 50 个事件，按 50 分页。
- 轮询：dashboard 刷新间隔按项目可配置（默认 30s）。scanner 不做 push。

## 性能

- scanner 按项目缓存文件读取，按 mtime 失效。
- task 数 > 100 的项目，列表只读 `task.json`；完整 evidence 扫描在打开 Task Detail 时进行。

## 非目标（v0 dashboard 契约）

- ROI / 成本核算 dashboard。
- 多项目 baseline 聚合。
- 实时 push（websocket）。仅轮询。
- spec freshness 检测（advisory 治理，独立子任务）。
- PRD/design 契约质量检查（advisory 治理，独立子任务）。
- 影响半径 / scope 溢出指标（advisory 治理，独立子任务）。
- 硬性 95 分 scoring 门禁（advisory scoring 放在第 2 个月）。
