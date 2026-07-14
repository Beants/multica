#!/usr/bin/env python3
"""
harness resume — 快速唤起终端续聊

用法:
  # 交互选择（给 parent issue）
  python3 harness/cli/resume.py XU-181

  # 直接续（给 child issue）
  python3 harness/cli/resume.py XU-183

  # 指定 profile
  python3 harness/cli/resume.py XU-181 --profile desktop-api.multica.ai

  # 只看命令不打开终端
  python3 harness/cli/resume.py XU-181 --dry-run
"""

import argparse
import json
import os
import subprocess
import sys
from pathlib import Path
from urllib.request import Request, urlopen

# ── provider → resume 命令映射 ──────────────────────────────────
RESUME_ARGS = {
    "claude":     lambda sid: [f"--resume", sid],
    "codebuddy":  lambda sid: [f"--resume", sid],
    "copilot":    lambda sid: [f"--resume", sid],
    "cursor":     lambda sid: [f"--resume", sid],
    "opencode":   lambda sid: [f"run", f"--session", sid],
    "hermes":     lambda sid: [],
    "pi":         lambda sid: [],
    "kimi":       lambda sid: [],
    "kiro":       lambda sid: [],
    "qoder":      lambda sid: [],
    "codex":      lambda sid: [],
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


def load_config(profile):
    if profile == "default":
        config_path = Path.home() / ".multica" / "config.json"
    else:
        config_path = Path.home() / ".multica" / "profiles" / profile / "config.json"
    if not config_path.exists():
        print(f"找不到配置: {config_path}")
        sys.exit(1)
    with open(config_path) as f:
        return json.load(f)


def api_get(config, path):
    server = config["server_url"].rstrip("/")
    token = config["token"]
    url = f"{server}{path}"
    req = Request(url)
    req.add_header("Authorization", f"Bearer {token}")
    req.add_header("Accept", "application/json")
    try:
        with urlopen(req, timeout=10) as resp:
            return json.loads(resp.read().decode())
    except Exception as e:
        print(f"API 请求失败: {e}")
        sys.exit(1)


def get_runtime_map(config):
    runtimes = api_get(config, "/api/runtimes")
    result = {}
    for r in (runtimes if isinstance(runtimes, list) else []):
        rid = r.get("id", "")
        atype = r.get("agent_type", "") or r.get("name", "").lower()
        result[rid] = atype.lower()
    return result


def get_issue(config, issue_key):
    return api_get(config, f"/api/issues/{issue_key}")


def get_children(config, parent_id):
    data = api_get(config, f"/api/issues/{parent_id}/children")
    if isinstance(data, list):
        return data
    if isinstance(data, dict):
        return data.get("issues", data.get("children", []))
    return []


def get_agent(config, agent_id):
    if not agent_id:
        return {}
    return api_get(config, f"/api/agents/{agent_id}")


def build_command(provider, session_id):
    binary = CLI_BINARIES.get(provider, "claude")
    arg_fn = RESUME_ARGS.get(provider, RESUME_ARGS["claude"])
    args = arg_fn(session_id)
    parts = [binary] + args
    return " ".join(parts)


def open_terminal(work_dir, command):
    safe_cmd = command.replace('"', '\\"')
    safe_dir = work_dir.replace('"', '\\"')
    script = (
        f'tell application "Terminal"\n'
        f'  activate\n'
        f'  do script "cd \\"{safe_dir}\\" && {safe_cmd}"\n'
        f'end tell'
    )
    subprocess.run(["osascript", "-e", script], check=False)


def collect_sessions(config, issue_key, runtime_map):
    """从 issue（可能是 parent 或 child）收集所有可续聊的 session"""
    issue = get_issue(config, issue_key)
    parent_id = issue.get("parent_issue_id")

    if parent_id:
        # 传进来的是 child → 直接收集这一个
        parent = get_issue(config, parent_id)
        children = get_children(config, parent_id)
        target_issue = issue
    else:
        # 传进来的是 parent → 收集所有 child
        parent = issue
        issue_id = issue.get("id", "")
        children = get_children(config, issue_id)
        target_issue = None

    sessions = []

    for child in children:
        child_id = child.get("id", "")
        child_key = child.get("identifier", child_id[:8])
        title = child.get("title", "")
        stage = child.get("stage") or "-"
        status = child.get("status", "")
        session_id = child.get("prior_session_id", "")
        work_dir = child.get("work_dir", "") or child.get("prior_work_dir", "")
        assignee_id = child.get("assignee_id", "")
        agent = get_agent(config, assignee_id)
        agent_name = agent.get("name", "")
        runtime_id = agent.get("runtime_id", "")
        provider = runtime_map.get(runtime_id, "claude")

        sessions.append({
            "key": child_key,
            "title": title,
            "stage": stage,
            "status": status,
            "agent": agent_name,
            "provider": provider,
            "session_id": session_id,
            "work_dir": work_dir,
            "has_session": bool(session_id and work_dir),
        })

    return parent, sessions, target_issue


def interactive_select(sessions):
    """交互选择续聊哪个 child"""
    available = [s for s in sessions if s["has_session"]]
    if not available:
        print("没有可续聊的 session（所有 child 都没跑过或 session 过期了）")
        sys.exit(1)

    print()
    for i, s in enumerate(available):
        print(f"  [{i+1}] Stage {s['stage']}  {s['agent']:20}  {s['title']}")
        print(f"      session={s['session_id'][:12]}...  status={s['status']}")
    print(f"  [q] 退出")
    print()

    choice = input(f"选哪个续聊？(1-{len(available)}/q): ").strip()
    if choice == "q" or not choice:
        sys.exit(0)
    try:
        idx = int(choice) - 1
        return available[idx]
    except (ValueError, IndexError):
        print("无效选择")
        sys.exit(1)


def main():
    parser = argparse.ArgumentParser(description="快速唤起终端续聊 Multica agent session")
    parser.add_argument("issue", help="Issue identifier（如 XU-181）或 child identifier（如 XU-183）")
    parser.add_argument("--profile", default="desktop-api.multica.ai", help="Multica profile")
    parser.add_argument("--dry-run", action="store_true", help="只打印命令不打开终端")
    args = parser.parse_args()

    config = load_config(args.profile)
    print(f"Server: {config['server_url']}")

    runtime_map = get_runtime_map(config)

    print(f"查询 issue {args.issue} ...")
    parent, sessions, target_child = collect_sessions(config, args.issue, runtime_map)

    print(f"Parent: {parent.get('identifier', '')} — {parent.get('title', '')}")
    print(f"Children: {len(sessions)} 个\n")

    # 选择
    if target_child:
        # 传的是 child → 直接用
        matching = [s for s in sessions if s["key"] == args.issue]
        if not matching:
            # 可能是 UUID
            matching = [s for s in sessions if s["key"] in args.issue]
        if not matching:
            print(f"找不到 child {args.issue}")
            sys.exit(1)
        selected = matching[0]
    else:
        # 传的是 parent → 交互选
        selected = interactive_select(sessions)

    if not selected["has_session"]:
        print(f"⚠️  {selected['key']} 没有 session_id，无法续聊")
        sys.exit(1)

    command = build_command(selected["provider"], selected["session_id"])

    print(f"\n  Issue:    {selected['key']} — {selected['title']}")
    print(f"  Agent:    {selected['agent']}")
    print(f"  Provider: {selected['provider']}")
    print(f"  Session:  {selected['session_id'][:16]}...")
    print(f"  WorkDir:  {selected['work_dir']}")
    print(f"  Command:  {command}")

    if args.dry_run:
        print("\n(dry-run，不打开终端)")
        return

    open_terminal(selected["work_dir"], command)
    print("\n✅ 已打开终端，开始续聊吧。")


if __name__ == "__main__":
    main()
