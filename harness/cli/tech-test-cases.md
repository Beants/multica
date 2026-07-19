# tech-test-cases.md — harness resume.py 技术侧测试用例

> 从实际实现推导。**v2：work_dir-only**（按队长方向调整，放弃 session_id 获取）。
> 覆盖集成点、真实 API 签名、数据流、错误路径。

## 设计基线（v2 变更）

**策略变更（队长 069222ea）：** 放弃 session_id 获取链路，改 work_dir-only。
- 链路：issue → assignee(agent) → children → 每个 child 查 agent 最新 task → **只取 `work_dir`**。
- 续聊：打开 Terminal.app → `cd work_dir` → 跑 provider 命令。**CLI 不传 `--resume <id>`**。session 连续性交给 provider 自身 cwd 机制（或后续 daemon claim 的 `prior_session_id`）。
- 可续聊 ⇔ 最新 task 有 `work_dir`。
- v1 的 session_id 反查（opencode SQLite / claude 本地扫描）**已全部移除**。

## 真实数据流（实测确认）
```
issue identifier (XU-181)
  → GET /api/issues/<id>            [需 X-Workspace-ID header]
    返回 id(UUID) / parent_issue_id / assignee_id / assignee_type
  → GET /api/issues/<UUID>/children [需 X-Workspace-ID header]
    返回 { issues: [...] }（扁平数组，每个 issue 自带 stage；CLI 的 {stages} 是客户端分组，两形态兼容）
  → GET /api/agents/<agent_id>/tasks
    返回 task[]，取 issue_id 匹配的最新 task.work_dir
  → provider 来自 GET /api/runtimes[] 的 provider 字段
  → 打开 Terminal.app：cd work_dir && <provider 续聊命令>
```

## provider 续聊命令映射（work_dir-only，实测）
| provider | 命令 | 续聊机制 | 实测 |
|---|---|---|---|
| claude | `claude -c` | "Continue the most recent conversation in [cwd]" | ✅ `claude --help` 确认 `-c, --continue` |
| codebuddy/copilot/cursor | `<bin> -c` | 同族类比 | ⚠️ 本机未装，按 claude 族类比 |
| opencode | `opencode run -c` | "continue the last session" | ✅ `opencode run --help` 确认 `-c, --continue` |
| hermes/pi/kimi/kiro/qoder/codex | 裸 `<bin>` | ACP/线程协议，不传参数 | ✅ pi/codex/qodercli 本机已装，ACP 不传参 |

---

## TC-T01: identifier 解析（identifier + UUID 双形态）
- **类型**: 集成
- **输入**: `XU-181` / `18f69126-...-20dd373cf094`
- **预期**: 服务端 `/api/issues/<ref>` 两种都接受，命中同一 issue
- **结果**: ✅ 实测两者均 200

## TC-T02: workspace 上下文必需
- **类型**: 集成 / 错误路径
- **输入**: 不带 `X-Workspace-ID` header
- **预期**: 400 `{"error":"workspace_id or workspace_slug is required"}`
- **结果**: ✅ 实测；resume.py 每请求注入该 header

## TC-T03: macOS SSL 根证书
- **类型**: 环境 / 集成
- **预期**: framework Python 默认 `CERTIFICATE_VERIFY_FAILED` → 加载 `/etc/ssl/cert.pem` 修复
- **结果**: ✅ 全 stdlib（`_ssl_context()`）

## TC-T04: children 响应形态兼容
- **类型**: 集成
- **预期**: 原始 `{issues:[…]}` 与 CLI 态 `{stages,unstaged}` 都扁平化
- **结果**: ✅ XU-181 返回 3 child

## TC-T05: provider 解析（runtime.provider）
- **类型**: 集成
- **预期**: runtime 806f9edb→opencode，92195c8e→pi
- **结果**: ✅ 用 `provider` 字段（旧版误用 agent_type/name）

## TC-T06: work_dir 取数（核心链路）
- **类型**: 集成 / 数据流
- **输入**: child XU-183 → agent 6131a81d → 最新 task 36a07ead
- **预期**: `work_dir=/Users/xu/.../36a07ead/workdir`
- **结果**: ✅ 实测命中

## TC-T07: provider 续聊命令（work_dir-only，不传 session_id）★v2 核心
- **类型**: 功能 / 数据流
- **预期**: claude→`claude -c`；opencode→`opencode run -c`；ACP→裸 `<bin>`
- **结果**: ✅ `build_command` 11 个 provider 全部输出正确，无 `--resume`/`--session`

## TC-T08: parent 交互选择（AC1）
- **类型**: 功能
- **输入**: `resume XU-181 --dry-run` 选 2
- **预期**: 列出 3 child，选中 XU-183，`Command: pi`，不开终端
- **结果**: ✅

## TC-T09: child 直接续（AC4）
- **类型**: 功能
- **输入**: `resume XU-183 --dry-run`
- **预期**: 非交互，`Command: pi`
- **结果**: ✅

## TC-T10: --list 只列不执行（AC2）
- **类型**: 功能
- **输入**: `resume XU-181 --list`
- **预期**: 3 child + ✅可续聊（按 work_dir）+ provider，exit 0 不进选择
- **结果**: ✅

## TC-T11: 无 work_dir 提示（AC5）★v2 调整
- **类型**: 错误路径
- **输入**: child 最新 task 无 work_dir
- **预期**: `⚠️ <key> 没有 work_dir（task 未跑完）。该 issue 没有 session。` exit 1
- **结果**: ✅ guard 文本确认；真实无-work_dir issue 存在（2adcf762 本 issue 的 leader task）
- **注**: v1 是「无 session_id 提示」；v2 改为「无 work_dir 提示」，措辞对齐队长要求

## TC-T12: Terminal.app 命令生成（AC3）
- **类型**: 集成 / 平台
- **预期**: osascript `do script "cd \"<work_dir>\" && <cmd>"`
- **结果**: ✅ 三形态（pi 裸 / `claude -c` / `opencode run -c`）osascript 字符串均合法
- **边界**: work_dir 含双引号会破坏转义（真实 work_dir 不含引号，可接受）

## TC-T13: task 列表缓存（性能）
- **类型**: 性能
- **预期**: 同 agent 多 child 共享一次 `/api/agents/{id}/tasks`（单次 ~20s/584KB）
- **结果**: ✅ `_tasks_cache` 按 agent_id 缓存

## TC-T14: squad-assigned child 守卫
- **类型**: 错误路径 / 局限
- **预期**: `assignee_type != agent` → note「assignee 非 agent」，不计入可续聊
- **结果**: ✅ 代码分支存在；局限：无法定位 squad 委派给哪个 agent（无 issue→tasks API）

## TC-T15: work_dir 磁盘存在性 ★v3 修正（原为非阻断提醒，真机验收发现缺陷）
- **类型**: 错误路径（回归）
- **背景**: v2 里 work_dir 磁盘不存在只 `print` 警告后照常 `open_terminal`，弹出 `cd <不存在目录> && cmd` 的坏终端；`has_session=bool(work_dir)` 只看字段不看磁盘，把已清理 session 标成 ✅可续聊。真机验收（队长）暴露。
- **预期（修正后）**:
  1. `collect_sessions`: `has_session = bool(work_dir) and os.path.isdir(work_dir)`，note 区分「无 work_dir」/「已被清理」
  2. `main`: work_dir 磁盘不存在 → `die("…work_dir 在磁盘上不存在（已被清理）…该 issue 没有 session。")` 硬停，绝不 `open_terminal`
- **结果**:
  - ✅ XU-181 `--list`：3 child 现全显 ❌ + note「work_dir 已被清理」（修复前显 ✅）
  - ✅ XU-183 `--dry-run`：`die` 硬停，exit=1，输出 0 行 `Command:`（未触 open_terminal）
  - ✅ LIVE work_dir（XU-185，…/47182f7a/workdir）：`os.path.isdir=True`，shell `cd` OK，main 两道 guard 均 pass → `build_command: hermes` 正常推进
  - ✅ DELETED work_dir（XU-183，…/36a07ead/workdir）：`os.path.isdir=False`，shell `cd` FAIL → 硬停
  - ✅ gate 正确性：`os.path.isdir` 与 shell `cd` 完全等价（live=True/OK，deleted=False/FAIL）
  - ✅ 真 Terminal.app 开窗在 live work_dir（osascript exit=0，window id 107985）

## TC-T16: 续聊机制可信度分级 ★v2 新增
- **类型**: 设计 / 验证完整性
- **预期**: 各 provider「只给 work_dir 如何续上」的实测结论入档
- **结果**: claude `-c`（help 实证）、opencode `run -c`（help 实证）、ACP 裸命令（实证）三类确认；codebuddy/copilot/cursor `-c` 为类比假设（本机未装，记 gap）
