#!/usr/bin/env python3
"""sync_agents.py — 把 harness squad-briefing + role prompts 同步到 multica role agent 的 instructions。

每个 role agent 的 instructions = squad-briefing（全队共享上下文）+ 该角色 prompt（专属职责）。
role→agent 按 name 模糊匹配（planner→规划员-Planner，gate-runner→门禁执行器-GateRunner）。
幂等：重复跑覆盖为最新 harness 文本。

用法:
    python3 harness/cli/sync_agents.py --profile desktop-api.multica.ai   # 真跑
    python3 harness/cli/sync_agents.py --dry-run                          # 只打印
    python3 harness/cli/sync_agents.py --role planner                     # 只更新一个角色

前提: 已 multica login 且 role agent 已存在（agent list 能匹配到）。
"""
from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]  # harness/
SQUAD = ROOT / "squad-briefing.md"

# role -> harness role prompt 文件
ROLE_PROMPTS = {
    "leader": ROOT / "leader" / "leader-prompt.md",
    "planner": ROOT / "skills" / "planner" / "prompt.md",
    "implementer": ROOT / "skills" / "implementer" / "prompt.md",
    "reviewer": ROOT / "skills" / "reviewer" / "prompt.md",
    "gate-runner": ROOT / "skills" / "gate-runner" / "prompt.md",
}

_PROFILE = ""


def run_mc(args: list[str], dry_run: bool) -> tuple[int, str, str]:
    cmd = ["multica", *(["--profile", _PROFILE] if _PROFILE else []), *args]
    if dry_run:
        print(f"  $ {' '.join(cmd[:3])} ... agent update --instructions <squad+role>")
        return 0, "", ""
    r = subprocess.run(cmd, capture_output=True, text=True)
    return r.returncode, r.stdout, r.stderr


def _norm(s: str) -> str:
    return re.sub(r"[-\s_]+", "", s.lower())


def _match_agent(role: str, agents: dict[str, str]) -> str | None:
    needle = _norm(role)
    for name, aid in agents.items():
        if needle in name:
            return aid
    return None


def main() -> int:
    ap = argparse.ArgumentParser(description="Sync harness squad+role prompts -> multica agent instructions.")
    ap.add_argument("--profile", help="multica CLI profile (e.g. desktop-api.multica.ai)")
    ap.add_argument("--dry-run", action="store_true")
    ap.add_argument("--role", help="Sync only this role (leader/planner/implementer/reviewer/gate-runner)")
    args = ap.parse_args()

    global _PROFILE
    _PROFILE = args.profile or ""

    if not SQUAD.is_file():
        print(f"error: {SQUAD} not found", file=sys.stderr)
        return 2
    squad = SQUAD.read_text(encoding="utf-8")

    code, out, _ = run_mc(["agent", "list", "--output", "json"], args.dry_run)
    if code != 0:
        print("error: agent list failed", file=sys.stderr)
        return 2
    try:
        rows = json.loads(out)
        rows = rows.get("data", rows) if isinstance(rows, dict) else rows
    except json.JSONDecodeError:
        rows = []
    agents: dict[str, str] = {}
    for r in rows or []:
        if isinstance(r, dict) and r.get("name") and r.get("id"):
            agents.setdefault(_norm(r["name"]), r["id"])

    failures = 0
    for role, ppath in ROLE_PROMPTS.items():
        if args.role and role != args.role:
            continue
        if not ppath.is_file():
            print(f"  skip   {role}: prompt {ppath} 不存在")
            continue
        aid = _match_agent(role, agents)
        if not aid:
            print(f"  skip   {role}: cloud 无匹配 agent（名字不含 '{role}'）")
            continue
        role_prompt = ppath.read_text(encoding="utf-8")
        # 全队共识在前，角色专属在后
        instructions = f"{squad}\n\n---\n\n{role_prompt}"
        c, _, e = run_mc(["agent", "update", aid, "--instructions", instructions], args.dry_run)
        if c != 0:
            print(f"  fail   {role}: {e.strip()[:120]}", file=sys.stderr)
            failures += 1
        else:
            print(f"  sync   {role:12} -> agent {aid[:8]} ({len(instructions)} chars: squad+role)")

    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
