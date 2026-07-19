#!/usr/bin/env python3
"""
harness resume — 快速唤起终端续聊 Multica agent session

用法:
  # 交互选择（给 parent issue）
  python3 harness/cli/resume.py XU-181

  # 直接续（给 child issue）
  python3 harness/cli/resume.py XU-183

  # 指定 profile
  python3 harness/cli/resume.py XU-181 --profile desktop-api.multica.ai

  # 只看命令不打开终端
  python3 harness/cli/resume.py XU-181 --dry-run

  # 只列出可续聊的 session
  python3 harness/cli/resume.py XU-181 --list

数据链路（work_dir-only，实测确认）:
  issue identifier
    -> GET /api/issues/<id>            拿 parent_issue_id + assignee（需 X-Workspace-ID header）
    -> GET /api/issues/<id>/children   拿各 child issue（原始 HTTP 是扁平 {issues:[…]}）
    -> 每个 child 的 assignee(agent) GET /api/agents/<agent_id>/tasks  拿最新 task 的 work_dir
    -> 打开 Terminal.app：cd work_dir && 跑 provider 续聊命令

续聊机制（不依赖 session_id）:
  Multica 服务端 API 从不对外暴露 session_id（它只在 daemon claim 时以
  prior_session_id 返给 daemon）。本 CLI 改用 provider 自身的 cwd 续聊机制：
    claude/codebuddy/copilot/cursor -> `<bin> -c`   （continue 最近一条本目录会话）
    opencode                        -> `opencode run -c`
    ACP(hermes/pi/kimi/kiro/qoder/codex) -> 裸 `<bin>`（协议续聊，不传参数）
  可续聊 ⇔ 该 child 的最新 task 有 work_dir。
"""

import argparse
import json
import os
import subprocess
import sys
from pathlib import Path
from urllib.error import HTTPError
from urllib.parse import urlencode
from urllib.request import Request, urlopen

# ── provider → 续聊命令映射（work_dir-only，不传 session_id）─────
# claude 系用 `-c/--continue`（实测 claude: "Continue the most recent
# conversation in [cwd]"；codebuddy/copilot/cursor 按同族类比，本机未装）；
# opencode 用 `run -c/--continue`（实测 "continue the last session"）；
# ACP 类（hermes/pi/kimi/kiro/qoder/codex）靠协议续聊，不传 CLI 参数。
RESUME_ARGS = {
    "claude": ["-c"],
    "codebuddy": ["-c"],
    "copilot": ["-c"],
    "cursor": ["-c"],
    "opencode": ["run", "-c"],
    "hermes": [],
    "pi": [],
    "kimi": [],
    "kiro": [],
    "qoder": [],
    "codex": [],
}

CLI_BINARIES = {
    "claude": "claude",
    "codebuddy": "codebuddy",
    "copilot": "copilot",
    "cursor": "cursor-agent",
    "opencode": "opencode",
    "hermes": "hermes",
    "pi": "pi",
    "kimi": "kimi",
    "kiro": "kiro-cli",
    "qoder": "qodercli",
    "codex": "codex",
}

DEFAULT_PROFILE = "desktop-api.multica.ai"


def die(msg, code=1):
    print(msg, file=sys.stderr)
    sys.exit(code)


# ── 配置 / SSL ──────────────────────────────────────────────────
def load_config(profile):
    if profile == "default":
        config_path = Path.home() / ".multica" / "config.json"
    else:
        config_path = Path.home() / ".multica" / "profiles" / profile / "config.json"
    if not config_path.exists():
        die(f"找不到配置: {config_path}")
    with open(config_path) as f:
        cfg = json.load(f)
    for k in ("server_url", "token", "workspace_id"):
        if not cfg.get(k):
            die(f"配置缺少 {k}: {config_path}")
    return cfg


def _ssl_context():
    """macOS 框架版 Python 常不自动加载系统根证书，显式加载 /etc/ssl/cert.pem
    （有 certifi 则优先用）。全程 stdlib。"""
    import ssl

    ctx = ssl.create_default_context()
    try:
        ctx.load_default_certs()
    except Exception:
        pass
    candidates = []
    try:
        import certifi  # type: ignore

        candidates.append(certifi.where())
    except Exception:
        pass
    candidates.append("/etc/ssl/cert.pem")  # macOS 系统证书束
    for c in candidates:
        if c and os.path.exists(c):
            try:
                ctx.load_verify_locations(c)
                return ctx
            except Exception:
                continue
    return ctx


# ── HTTP ────────────────────────────────────────────────────────
def api_get(config, path, params=None):
    server = config["server_url"].rstrip("/")
    url = server + path
    if params:
        url += ("&" if "?" in url else "?") + urlencode(params)
    req = Request(url)
    req.add_header("Authorization", f"Bearer {config['token']}")
    req.add_header("Accept", "application/json")
    req.add_header("X-Workspace-ID", config["workspace_id"])  # 服务端要求 workspace 上下文
    ctx = _ssl_context()
    try:
        with urlopen(req, timeout=15, context=ctx) as resp:
            body = resp.read().decode()
            return json.loads(body) if body else None
    except HTTPError as e:
        detail = ""
        try:
            detail = e.read().decode()[:200]
        except Exception:
            pass
        if e.code == 403:
            die(f"无权访问（私有 agent / 权限不足）: {path} {detail}")
        if e.code == 404:
            die(f"找不到资源: {path}")
        die(f"API 请求失败 [{e.code}] {path}: {detail}")
    except Exception as e:
        die(f"API 请求失败: {path}: {e}")


def get_issue(config, issue_ref):
    """issue_ref 可以是 identifier（XU-181）或 UUID。服务端两种都接受。"""
    return api_get(config, f"/api/issues/{issue_ref}")


def get_children(config, issue_id):
    """返回扁平的 child issue 列表。兼容两种响应形态：
    - 原始 HTTP：{'issues': [...]}（每个 issue 自带 stage 字段）
    - CLI 分组态：{'stages': [{stage, issues}], 'unstaged': [...]}
    """
    data = api_get(config, f"/api/issues/{issue_id}/children") or {}
    out = []
    if isinstance(data.get("issues"), list):
        for iss in data["issues"]:
            iss.setdefault("_stage", iss.get("stage"))
            out.append(iss)
        return out
    for st in data.get("stages", []) or []:
        stage = st.get("stage")
        for iss in st.get("issues", []) or []:
            iss.setdefault("_stage", stage)
            out.append(iss)
    for iss in data.get("unstaged", []) or []:
        iss.setdefault("_stage", None)
        out.append(iss)
    return out


def get_runtime_map(config):
    """runtime_id -> provider（取 runtime.provider 字段）。"""
    runtimes = api_get(config, "/api/runtimes")
    result = {}
    for r in runtimes if isinstance(runtimes, list) else []:
        rid = r.get("id", "")
        provider = (r.get("provider") or "").lower()
        if not provider:
            # 兜底：从 name 推（"Opencode (...)" -> opencode）
            provider = (r.get("name") or "").split("(")[0].strip().lower()
        result[rid] = provider
    return result


def latest_task_for(config, agent_id, issue_uuid):
    """该 agent 在该 issue 上最新的一条 task（按 completed_at/created_at 降序）。
    返回 task dict 或 None。task 里有 work_dir（completed 后由 daemon 回填）。
    task 列表按 agent_id 缓存（同一 parent 下多个 child 常共享同一 agent，
    且该接口会返回完整 result.output，单次几十 KB~MB，避免重复拉取）。"""
    if not agent_id:
        return None
    if agent_id not in _tasks_cache:
        tasks = api_get(config, f"/api/agents/{agent_id}/tasks")
        _tasks_cache[agent_id] = tasks if isinstance(tasks, list) else []
    tasks = _tasks_cache[agent_id]
    matching = [t for t in tasks if t.get("issue_id") == issue_uuid]
    if not matching:
        return None

    def sort_key(t):
        return t.get("completed_at") or t.get("started_at") or t.get("created_at") or ""

    matching.sort(key=sort_key, reverse=True)
    return matching[0]


_tasks_cache = {}


# ── 命令 / 终端 ──────────────────────────────────────────────────
def build_command(provider):
    """provider -> (binary, args)。work_dir-only：不传 session_id，
    靠 provider 自身的 cwd 续聊机制（claude 系 `-c`、opencode `run -c`、ACP 裸命令）。"""
    binary = CLI_BINARIES.get(provider, "claude")
    args = list(RESUME_ARGS.get(provider, RESUME_ARGS["claude"]))
    return binary, args


def open_terminal(work_dir, binary, args):
    """macOS Terminal.app：cd 到 work_dir 并执行 resume 命令。"""
    # 用 exec 形式避免 shell 注入；每个 token 单独转义。
    quoted = [binary] + [a.replace("'", "'\\''") for a in args]
    cmd_str = " ".join(quoted)
    safe_dir = work_dir.replace('"', '\\"')
    cmd_str_esc = cmd_str.replace('"', '\\"')
    script = (
        f'tell application "Terminal"\n'
        f'  activate\n'
        f'  do script "cd \\"{safe_dir}\\" && {cmd_str_esc}"\n'
        f'end tell'
    )
    subprocess.run(["osascript", "-e", script], check=False)


# ── 收集 session ────────────────────────────────────────────────
def collect_sessions(config, issue_ref, runtime_map):
    """从 issue（parent 或 child）收集可续聊 session。
    返回 (parent_issue, sessions, target_child_or_None)。"""
    issue = get_issue(config, issue_ref)
    issue_uuid = issue.get("id", "")
    parent_id = issue.get("parent_issue_id")

    if parent_id:
        # 传进来的是 child → 直接收集这一个
        parent = get_issue(config, parent_id)
        children = get_children(config, parent_id)
        target = issue
    else:
        parent = issue
        children = get_children(config, issue_uuid)
        target = None

    sessions = []
    for child in children:
        child_uuid = child.get("id", "")
        child_key = child.get("identifier", child_uuid[:8])
        stage = child.get("_stage") or child.get("stage") or "-"
        status = child.get("status", "")
        title = child.get("title", "")
        assignee_id = child.get("assignee_id", "")
        assignee_type = child.get("assignee_type", "")

        entry = {
            "key": child_key,
            "uuid": child_uuid,
            "title": title,
            "stage": stage,
            "status": status,
            "agent": "",
            "provider": "",
            "runtime_id": "",
            "work_dir": "",
            "has_session": False,
            "note": "",
        }

        if assignee_type != "agent" or not assignee_id:
            entry["note"] = f"assignee 非 agent（{assignee_type or '空'}），无法定位 task"
            sessions.append(entry)
            continue

        task = latest_task_for(config, assignee_id, child_uuid)
        if not task:
            entry["note"] = "该 agent 在此 issue 上无 task 记录"
            sessions.append(entry)
            continue

        runtime_id = task.get("runtime_id", "") or ""
        provider = runtime_map.get(runtime_id, "")
        if not provider:
            # task 自带 runtime，但 runtime_map 缺失（runtime 已离线）→ 兜底默认
            provider = "claude"
        work_dir = task.get("work_dir", "") or ""
        # 可续聊要求 work_dir 字段非空且磁盘上真实存在——本 CLI 跑在本地，
        # done issue 的 work_dir 约 24h 后被清理，已删的目录续不了，不能装作能续。
        work_dir_exists = bool(work_dir) and os.path.isdir(work_dir)

        entry.update({
            "agent": _agent_name(config, assignee_id),
            "provider": provider,
            "runtime_id": runtime_id,
            "work_dir": work_dir,
            "has_session": work_dir_exists,
            "note": (
                "" if work_dir_exists
                else ("work_dir 已被清理（done issue ~24h 删除），无法续聊" if work_dir
                      else "task 无 work_dir（可能未跑完）")
            ),
        })
        sessions.append(entry)

    return parent, sessions, target


_agent_name_cache = {}


def _agent_name(config, agent_id):
    if not agent_id:
        return ""
    if agent_id in _agent_name_cache:
        return _agent_name_cache[agent_id]
    name = ""
    try:
        a = api_get(config, f"/api/agents/{agent_id}")
        name = a.get("name", "") if isinstance(a, dict) else ""
    except Exception:
        name = ""
    _agent_name_cache[agent_id] = name
    return name


# ── 展示 / 选择 ──────────────────────────────────────────────────
def print_list(parent, sessions):
    pid = parent.get("identifier", "")
    ptitle = parent.get("title", "")
    print(f"\n  Parent: {pid} — {ptitle}\n")
    if not sessions:
        print("  （无 child issue）")
        return
    for i, s in enumerate(sessions, 1):
        mark = "✅可续聊" if s["has_session"] else "❌"
        wd = ("…" + s["work_dir"][-30:]) if s["work_dir"] else "(无 work_dir)"
        print(f"  [{i}] Stage {s['stage']}  {s['agent'][:18]:18}  {s['key']}  {s['status']}  {mark}")
        print(f"      work_dir={wd}  provider={s['provider'] or '?'}")
        if s["note"]:
            print(f"      note: {s['note']}")
    print()


def interactive_select(sessions):
    available = [s for s in sessions if s["has_session"]]
    if not available:
        print("没有可续聊的 session（所有 child 都没 work_dir）")
        sys.exit(1)
    print()
    for i, s in enumerate(available, 1):
        print(f"  [{i}] Stage {s['stage']}  {s['agent'][:18]:18}  {s['title'][:40]}")
        print(f"      {s['key']}  provider={s['provider']}  wd=…{s['work_dir'][-24:]}")
    print(f"  [q] 退出\n")
    choice = input(f"选哪个续聊？(1-{len(available)}/q): ").strip()
    if choice.lower() == "q" or not choice:
        sys.exit(0)
    try:
        return available[int(choice) - 1]
    except (ValueError, IndexError):
        die("无效选择")


def select_by_ref(sessions, ref):
    ref_l = ref.lower()
    for s in sessions:
        if s["key"].lower() == ref_l or s["uuid"].lower() == ref_l:
            return s
    for s in sessions:
        if ref_l in s["uuid"].lower():
            return s
    return None


# ── main ────────────────────────────────────────────────────────
def main():
    ap = argparse.ArgumentParser(
        prog="harness resume",
        description="快速唤起终端续聊 Multica agent session",
    )
    ap.add_argument("issue", help="issue identifier（XU-181）或 child identifier（XU-183）/ UUID")
    ap.add_argument("--profile", default=DEFAULT_PROFILE, help="Multica profile")
    ap.add_argument("--dry-run", action="store_true", help="只打印命令不打开终端")
    ap.add_argument("--list", action="store_true", help="只列出可续聊 session，不执行")
    args = ap.parse_args()

    config = load_config(args.profile)
    print(f"Server: {config['server_url']}  workspace: {config['workspace_id'][:8]}...")

    runtime_map = get_runtime_map(config)
    print(f"查询 issue {args.issue} ...")
    parent, sessions, target = collect_sessions(config, args.issue, runtime_map)

    print_list(parent, sessions)

    if args.list:
        return

    if target:
        # 传的是 child → 直接续这一个
        selected = select_by_ref(sessions, args.issue)
        if not selected:
            die(f"在 parent {parent.get('identifier')} 下找不到 child {args.issue}")
    else:
        selected = interactive_select(sessions)

    if not selected["work_dir"]:
        die(f"⚠️  {selected['key']} 没有 work_dir（task 未跑完）。该 issue 没有 session。")
    if not os.path.isdir(selected["work_dir"]):
        # work_dir 字段有值但磁盘不存在（done issue work_dir ~24h 被清理）。
        # 硬停，避免弹出一个 cd <不存在目录> && <cmd> 的坏终端。
        die(f"⚠️  {selected['key']} 的 work_dir 在磁盘上不存在（已被清理）：{selected['work_dir']}。该 issue 没有 session。")

    binary, cmd_args = build_command(selected["provider"])
    cmd_display = " ".join([binary] + cmd_args)

    print(f"\n  Issue:    {selected['key']} — {selected['title']}")
    print(f"  Agent:    {selected['agent']}")
    print(f"  Provider: {selected['provider']}")
    print(f"  WorkDir:  {selected['work_dir']}")
    print(f"  Command:  {cmd_display}")

    if args.dry_run:
        print("\n(dry-run，不打开终端)")
        return

    open_terminal(selected["work_dir"], binary, cmd_args)
    print("\n✅ 已打开终端，开始续聊吧。")


if __name__ == "__main__":
    main()
