# Multica Harness CLI — 开发任务书

> 本文档仅描述 `resume.py`（终端续聊）的设计。`cli/` 现另含 `sync_skills.py`（社区 skill 同步）与 `register_skills.py`（注册到 multica + bind role agent），详见 README「方法论 skill」节。

## 目标

写一个 Python CLI 工具 `harness`，让用户从命令行快速唤起终端，续聊 Multica 上某个 agent 的 session。

## 背景

Multica 是一个 AI Agent 协作平台（github.com/multica-ai/multica）。每个 task（agent 执行一轮）完成后会在 DB 记录 session_id。下次同一个 (agent, issue) 再 claim task 时，server 自动把 session_id 作为 `prior_session_id` 返回，daemon 自动 `--resume`。

但用户想**手动介入**时（比如 Spec 澄清阶段需要多轮高频对话），需要从 web UI 找到 session_id 和 work_dir，手动开终端拼命令，体验很差。

本工具就是把这个过程自动化。

## Multica 关键信息

### CLI 二进制

```
/Applications/Multica.app/Contents/Resources/app.asar.unpacked/resources/bin/multica
```

所有命令需要 `export PATH="/Applications/Multica.app/Contents/Resources/app.asar.unpacked/resources/bin:$PATH"`。

### Profile 配置

```
~/.multica/config.json                              # default profile
~/.multica/profiles/<profile-name>/config.json      # named profile
```

每个 config.json 包含：
```json
{
  "server_url": "https://api.multica.ai",
  "workspace_id": "4052d4e7-...",
  "token": "mul_150bbe8a..."
}
```

### 当前用的 profile

`desktop-api.multica.ai`，server 是 `https://api.multica.ai`。

### 源码位置（本地 fork）

```
/Users/xu/Documents/Projects/multica
```

关键源码文件：
- `server/internal/handler/daemon.go` — ClaimTask 时 `GetLastTaskSession(agent_id, issue_id)` 自动填 `prior_session_id`
- `server/internal/handler/agent.go` — task API 返回字段定义（L298: `PriorSessionID`, L299: `PriorWorkDir`, L300: `WorkDir`）
- `server/pkg/agent/agent.go` — `ExecOptions.ResumeSessionID`（L39）
- `server/pkg/agent/claude.go` — Claude: `--resume <id>`（L605）
- `server/pkg/agent/codex.go` — Codex: `thread/resume` 协议（L1088）
- `server/pkg/agent/hermes.go` — Hermes/ACP: `session/resume`（L235）

### API 认证

所有 API 请求带 header：
```
Authorization: Bearer <token>
```

### 核心 API 路径（需要实测确认）

CLI 命令 `multica issue get XU-181 --output json` 和 `multica agent tasks <agent-id> --output json` 能拿到数据。建议先抓 CLI 的实际 HTTP 请求来确认 API 路径——可以用 `multica --debug <command>` 或抓包。

关键数据链路：
1. 输入 issue identifier（如 XU-181）
2. 查 issue 详情 → 拿 `assignee_id`（agent）+ `parent_issue_id`
3. 如果是 parent issue → 查它的 children（每个 child 是一个 stage）
4. 对每个 child → 查 assignee agent → 查该 agent 在该 issue 上的最新 task
5. task 里有 `session_id` + `work_dir`
6. 如果 task 没有 `session_id` → 也可以用 `prior_session_id`（server claim 时填充）

注意：`session_id` 和 `prior_session_id` 可能在不同的 API 响应里。`session_id` 在 task 完成后写入；`prior_session_id` 在 claim task 时由 server 查 `GetLastTaskSession` 填充。需要实测哪个 API 返回哪个字段。

## 需求

### 命令 1: `harness resume <issue>` — 交互选择

```
$ harness resume XU-181

  Parent: XU-181 — Toy: 3-Stage Barrier Test

  [1] Stage 1  规划员-Planner      session=abc123...  status=done    ✅可续聊
  [2] Stage 3  实现员-Implementer   session=def456...  status=done    ✅可续聊
  [3] Stage 5  代码审查员-Reviewer   (无 session)                     ❌

  选哪个？(1-2/q):
```

选完后：
- 按 provider 拼命令
- 打开 macOS Terminal.app
- cd 到 work_dir
- 执行 resume 命令

### 命令 2: `harness resume <child-issue>` — 直接续

```
$ harness resume XU-183
```

直接拿这个 child 的 session，不交互选。

### 通用 flags

```
--profile <name>     # 默认 desktop-api.multica.ai
--dry-run            # 只打印命令不打开终端
--list               # 只列出可续聊的 session，不执行
```

## Provider → Resume 命令映射

从 `server/pkg/agent/*.go` 的 buildArgs 提取：

| Provider | CLI 命令 | Resume 参数 |
|---|---|---|
| claude | `claude` | `--resume <id>` |
| codebuddy | `codebuddy` | `--resume <id>` |
| copilot | `copilot` | `--resume <id>` |
| cursor | `cursor-agent` | `--resume <id>` |
| opencode | `opencode` | `run --session <id>` |
| hermes | `hermes` | (ACP session/resume，不传 CLI 参数) |
| pi | `pi` | (ACP) |
| kimi | `kimi` | (ACP) |
| kiro | `kiro-cli` | (ACP) |
| qoder | `qodercli` | (ACP) |
| codex | `codex` | (thread/resume 协议，不传 CLI 参数) |

## 技术要求

- **语言**：Python 3，零第三方依赖（只用 stdlib）
- **平台**：macOS（Terminal.app），留 Linux/Windows 扩展点
- **代码位置**：`/Users/xu/Documents/Projects/multica/harness/cli/`
- **入口**：`python3 harness/cli/resume.py` 或 `pip install -e harness/` 后 `harness resume`
- **错误处理**：session 过期 / work_dir 不存在 / API 超时，给出清晰提示
- **不要碰 Multica 源码**——纯调 API + 打开终端

## 验收标准

1. `harness resume XU-181 --dry-run` 输出正确的命令（不打开终端）
2. `harness resume XU-181` 列出所有 child + 可续聊状态
3. 选一个后打开 Terminal.app，cd 到 work_dir，执行正确的 resume 命令
4. `harness resume XU-183` 直接续（不交互选）
5. 无 session 时提示"该 issue 没有 session"

## 参考实现（半成品）

`/Users/xu/Documents/Projects/multica/harness/cli/resume.py` 有一个半成品，API 路径需要实测修正。主要问题是 `session_id` 的获取链路还没跑通。
