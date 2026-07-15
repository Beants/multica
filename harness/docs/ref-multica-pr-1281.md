# Multica PR #1281: Bridge External Codex Sessions into Issue Workflow

- 原文：https://github.com/multica-ai/multica/pull/1281
- 提交者：Jacobinwwey
- 开启时间：2026-04-17
- 状态：Open（未合并）
- 归档日期：2026-07-14

---

## Context

没有对应的 upstream issue。最接近的是 #1136，但 #1166 已修复 same-issue thread reuse。本 PR 覆盖 external-session discovery 和 manual-continue path。

## Problem

`main` 可以恢复 issue-scoped 任务的 Codex thread，但无法发现任意外部 Codex session 或将手动 Continue 操作映射回 issue workflow。

## Implementation

新增 API：

| 接口 | 用途 |
|---|---|
| `GET /api/agents/{id}/external-sessions` | 发现外部 CLI session |
| `POST /api/agents/{id}/resume-session` | 恢复 session |
| `POST /api/agents/{id}/tasks/{taskId}/resume` | 在 task 内恢复 |
| `POST /api/agents/{id}/tasks/{taskId}/bind-issue` | 把外部 session 绑回 issue |

- 合并 session-file discovery 与 live `codex resume` process inspection
- 持久化 `resume_session_id` / `resume_source` / `resume_command` 到 task context
- 在 task cards + issue live cards 展示 metadata

## Trade-offs

- 扫描 Codex session 文件 + live process table，不调用 interactive Codex 命令
- 保持容器内确定性，避免 CLI coupling
- 自托管部署需要可选的只读 host `.codex` mount

## 对设计文档的启示

这个 PR 证明了 Multica 社区在往 session resume 方向走，但：
1. 只覆盖 Codex，没覆盖 Claude / OpenCode
2. 还没合并（4 月开 PR，至今 Open）
3. 没有解决"headless → 人类介入 → headless 恢复"的两段式切换

我们的设计用 SessionID 作为唯一契约，比这个 PR 的方案更通用。
