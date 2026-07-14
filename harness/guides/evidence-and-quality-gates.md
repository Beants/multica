# Evidence and Quality Gates

> 在 Plan Self-Check 和 Spec Freeze 前使用本指南。每道门禁必须通过，或由 owner 显式延期。

## 门禁 1：Spec 完整性

`prd.md` 必须回答：

- 问题陈述
- 成功指标或可观测的成功信号
- 用户故事 / 受影响角色
- 验收标准
- 非目标
- 约束

若有意省略某项，在 `decision-log.md` 或 `open-questions.md` 中记录原因。

## 门禁 2：Source 覆盖

每条实质性论断都必须能追溯到下列之一：

- `source-index.md` 中的 source document ID
- 现有代码路径
- 现有测试或 eval 报告
- 外部 owner 决策
- 标记为待人工决策的显式假设

不要让会议纪要摘要替代缺失的上游契约、Jira 细节或 PRD 决策。

## 门禁 3：架构影响

`design.md` 或 `impact-map.md` 必须覆盖以下相关影响：

- 公开 API、schema、事件、prompt、工具、权限
- 数据迁移或兼容性
- 现有 `.trellis/spec/` 规则
- 可观测性、rollout、回滚
- 用户可见时的性能与并发

若某个设计与现有项目约定或 ADR 冲突，要作为决策点显式提出，而不是藏进实现步骤里。

## 门禁 4：任务拆解

每个子任务都必须能独立 review：

- 单一目标
- 尽量限定在有限文件或模块内
- 验收标准映射到测试或人工检查
- 声明对更早子任务的依赖
- 明确的回滚或延期路径

如果只是镜像实现分层、无法独立验证，就不要为此拆出子任务。

## 门禁 5：测试与 eval 证据

`implement.md` 必须列出证明完成所需的命令或证据：

- unit / integration / e2e / eval 命令
- 预期通过标准
- 所需 fixture 或环境
- 已知缺口及其接受人

对智能体行为变更，附上回归场景 ID、可得时的 pass^k 或 daily-eval 期望，以及回滚标准。

## 门禁 6：review 证据

一条 review finding 只有在带有至少一个证据 handle 时才有效：

- 可得时的文件路径与行号
- 命令与结果
- source document ID
- MR URL 或分支
- 解释证据为何不可得的显式限制

阻断型 finding 必须映射到 PRD、design、implement、spec、test、security、data 或 rollout 期望。

## 门禁 7：反糊弄（anti-slop）

拒绝含糊或自夸的论断。

必须改写的例子：

- "looks good"
- "should work"
- "probably covered"
- "simple change"
- 没有 fresh 验证证据的 "all set"

用具体证据替换它们：

- 检查了什么
- 命令或来源
- 结果
- 限制或残余风险

## 门禁 8：证据响应

当用户问某事是否被测试、review、执行、push 或更新过时，用证据回答：

- 检查的命令或来源
- 结果
- 覆盖范围
- 缺口或未跑项
- 证据缺失时的下一步动作

如果检查没跑，直接说没跑，要么跑它、要么解释阻塞原因。不要用 "looks good" 或 "should be fine" 没证据地回答。

## 门禁 9：密钥与环境处理

涉及 token、API key、`.env`、网关或本地 CLI 配置的工作：

- 绝不把密钥值写进 Trellis artifact、报告、MR 描述或摘要
- 只记录密钥来源，如 `.env`、本地 CLI 配置或聊天中由用户提供
- 保留命令证据，但脱敏密钥值
- 把环境事实和用户更正记进 task context、notes，或当 task 事实密集时记进可选的 `runtime-facts.md`
- 经 review 后才把耐用的环境约定提升到 `.trellis/spec/`

## 门禁 10：context 更新

在 Finish 前，或接受重要规划/review 证据后，判断工作是否暴露了耐用的 context：

- 新增或变更的业务规则
- 新的 API、schema、事件、prompt 或工具契约
- 模块边界或归属变更
- 反复出现的失败模式
- 值得复用的校验或 rollout 约定

若有，在下列之一记录一个候选更新：

- `.trellis/spec/`，走正常的 Trellis spec-update 流程
- 项目 `docs/`，若是产品、API 或运维文档
- task `eval/` 或 `reviews/`，若仅为本 task 的证据

不要另建长期记忆库。接受的 context 更新必须经 Code CLI review 后落入 Trellis 或项目文件。

## Evidence Runtime Protocol (v0)

evidence runtime 让门禁 5/6/8 可被机器读取。`.trellis/tasks/<task>/` 下有两种文件：

### `gate-result.jsonl`（append-only 事件日志）

每个 gate 事件一行 JSON。完整 schema 见 [Baseline and Gate-Result Protocol](./baseline-and-gate-result-protocol.md)。硬门禁（`hard: true`）可阻断；软门禁（`hard: false`）只留疤，不阻断。

### `baseline/{before,after,diff}.json`（回归证据）

在实现前后快照失败集合。diff 只对 `new_failures` 阻断，从不对 `known_failures` 阻断。见 [Baseline and Gate-Result Protocol](./baseline-and-gate-result-protocol.md)。

### Canonical 命令序列

```bash
# 实现前
python3 ./.trellis/workflows/ai-native-harness-dev/scripts/baseline.py snapshot \
  --task <task> --phase before --commands "<baseline-commands>" --if-missing

# 在此处进行实现

# 实现后
python3 ./.trellis/workflows/ai-native-harness-dev/scripts/baseline.py snapshot \
  --task <task> --phase after --commands "<same-baseline-commands>"
python3 ./.trellis/workflows/ai-native-harness-dev/scripts/baseline.py diff --task <task>

# 记录任意 gate 结果
python3 ./.trellis/workflows/ai-native-harness-dev/scripts/gate_result.py append \
  --task <task> --phase check --gate typecheck \
  --command "pnpm typecheck" --status pass \
  --duration-ms 1200 --hard --summary "exit 0"
```

dashboard（独立包）消费这些文件。智能体和人都必须把它们当作证据，而非状态。缺失文件必须当作 `unknown`，不是 `failed`。

## 失败处理

如果某道门禁失败：

1. 把失败的 gate 记进 `reviews/plan-self-check.md`。
2. 修复 artifact、跑一轮聚焦的研究循环，或提出最小化的人工问题。
3. 在失败被解决或被 owner 显式延期之前，不要请求 Spec Freeze。
