#!/usr/bin/env python3
"""register_skills.py — 把同步到本地的 skill 注册成 multica workspace skill。

配合 sync_skills.py：先 sync 拉取，再 register 注册到 multica。注册后 skill 成为
workspace 级实体，可在 UI bind 到对应 role agent（agent.skills）。task dispatch 时
multica daemon 把 SKILL.md 写进 workdir 的 provider 原生 skill 目录（.claude/skills/
、.pi/skills/ …），agent CLI 按 frontmatter description 自动触发（progressive
disclosure）——所以 role prompt 不需要引用 skill，CLI 自己发现。

幂等：按 name 匹配现有 skill，存在则 update 内容，不存在则 create。

用法:
    python3 harness/cli/register_skills.py            # 注册/更新所有 skill
    python3 harness/cli/register_skills.py --dry-run   # 只打印将执行的 multica 命令
    python3 harness/cli/register_skills.py --skill brainstorming  # 只注册一个

前提: 已 `multica login` 且选中目标 workspace（`multica workspace switch`）。
"""
from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]  # harness/
REGISTRY = ROOT / "skills" / "registry.json"

FM_RE = re.compile(r"^---\s*\n(.*?)\n---\s*\n", re.DOTALL)


_PROFILE = ""  # set from --profile; injected before each `multica` invocation


def run_mc(args: list[str], dry_run: bool = False) -> tuple[int, str, str]:
    """Run a `multica ...` command. Returns (exit, stdout, stderr)."""
    cmd = ["multica", *(["--profile", _PROFILE] if _PROFILE else []), *args]
    if dry_run:
        print("  $ " + " ".join(cmd))
        return 0, "", ""
    r = subprocess.run(cmd, capture_output=True, text=True)
    return r.returncode, r.stdout, r.stderr


def list_existing() -> dict[str, str]:
    """Return {skill_name: skill_id} for skills already in this workspace."""
    code, out, err = run_mc(["skill", "list", "--output", "json"])
    if code != 0:
        print(f"  warning: skill list failed ({err.strip()[:120]})", file=sys.stderr)
        return {}
    try:
        data = json.loads(out)
    except json.JSONDecodeError:
        return {}
    rows = data.get("data") if isinstance(data, dict) else data
    if not isinstance(rows, list):
        return {}
    return {r.get("name", ""): r.get("id", "") for r in rows if isinstance(r, dict) and r.get("name")}


def parse_frontmatter(md_path: Path) -> tuple[str, str]:
    """Extract name + description from a SKILL.md YAML frontmatter."""
    text = md_path.read_text(encoding="utf-8")
    m = FM_RE.match(text)
    if not m:
        return md_path.parent.name, ""
    block = m.group(1)

    def field(key: str) -> str:
        mm = re.search(rf'^{key}\s*:\s*(.+?)\s*$', block, re.MULTILINE)
        if not mm:
            return ""
        val = mm.group(1).strip()
        if (val.startswith('"') and val.endswith('"')) or (val.startswith("'") and val.endswith("'")):
            val = val[1:-1]
        return val

    return field("name") or md_path.parent.name, field("description")


def upsert_skill(entry: dict, existing: dict[str, str], dry_run: bool) -> str | None:
    """Create or update one skill in multica. Returns the multica skill id (or None).

    Supports two kinds of skill:
    - Community-synced (repo != null): SKILL.md + optional supporting files from upstream.
    - Self-authored (repo == null): SKILL.md + bundled files maintained locally.
    Both go through the same create/update + file-upsert path.
    """
    name = entry["name"]
    role = entry["role"]
    skill_dir = ROOT / "skills" / role / name
    md = skill_dir / "SKILL.md"
    if not md.is_file():
        print(f"  skip   {role}/{name}: SKILL.md not found locally" +
              (" (run sync first)" if entry.get("repo") else ""), file=sys.stderr)
        return None

    fm_name, description = parse_frontmatter(md)
    content_file = str(md)

    sid = existing.get(fm_name) or existing.get(name)
    if sid:
        code, _, err = run_mc(["skill", "update", sid, "--content-file", content_file], dry_run)
        if code != 0:
            print(f"  fail   {role}/{name}: skill update failed: {err.strip()[:120]}", file=sys.stderr)
            return None
        action = "update"
    else:
        args = ["skill", "create", "--name", fm_name, "--content-file", content_file]
        if description:
            args += ["--description", description]
        code, out, err = run_mc(args, dry_run)
        if code != 0:
            print(f"  fail   {role}/{name}: skill create failed: {err.strip()[:120]}", file=sys.stderr)
            return None
        if not dry_run:
            try:
                sid = json.loads(out).get("id", "")
            except json.JSONDecodeError:
                sid = ""
        else:
            sid = "(dry-run)"
        action = "create"

    # Attach supporting files (everything in registry `files` except SKILL.md).
    # For self-authored skills (repo == null), files are relative to the skill
    # directory (e.g. gates/foo.py → skills/<role>/<name>/gates/foo.py).
    # For community skills, files are relative to the skill directory too
    # (e.g. testing-anti-patterns.md → skills/<role>/<name>/testing-anti-patterns.md).
    for fpath in entry.get("files", []):
        basename = Path(fpath).name
        if basename == "SKILL.md":
            continue
        local = skill_dir / fpath
        if not local.is_file():
            print(f"  warn   {role}/{name}: file {fpath} not found at {local}", file=sys.stderr)
            continue
        # path within the skill (relative to skill root, not including skill name)
        rel = fpath
        code, _, err = run_mc(
            ["skill", "files", "upsert", sid, "--path", rel, "--content-file", str(local)],
            dry_run,
        )
        if code != 0:
            print(f"  warn   {role}/{name}: file upsert {rel} failed: {err.strip()[:120]}", file=sys.stderr)

    label = f"{role}/{fm_name}"
    kind = "local" if not entry.get("repo") else "synced"
    print(f"  {action:6} [{kind}] {label}  → {sid or '(dry-run)'}")
    return sid


def _norm_name(s: str) -> str:
    return re.sub(r"[-\s_]+", "", s.lower())


def _match_agent(role: str, agents: dict[str, str]) -> str | None:
    needle = _norm_name(role)
    for name, aid in agents.items():
        if needle in name:
            return aid
    return None


def bind_skills(reg: dict, dry_run: bool) -> None:
    """Bind each role's registered skills to the matching multica agent
    (fuzzy role<->agent-name match, e.g. planner -> '规划员-Planner')."""
    code, out, _ = run_mc(["agent", "list", "--output", "json"], dry_run)
    if code != 0:
        print("  bind skipped: agent list unavailable", file=sys.stderr)
        return
    try:
        rows = json.loads(out)
        rows = rows.get("data", rows) if isinstance(rows, dict) else rows
    except json.JSONDecodeError:
        rows = []
    agents: dict[str, str] = {}
    for r in rows or []:
        if isinstance(r, dict) and r.get("name") and r.get("id"):
            agents.setdefault(_norm_name(r["name"]), r["id"])

    by_role: dict[str, list[str]] = {}
    for s in reg["skills"]:
        if s.get("multica_id"):
            by_role.setdefault(s["role"], []).append(s["multica_id"])

    for role, ids in by_role.items():
        aid = _match_agent(role, agents)
        if not aid:
            print(f"  bind skip  {role}: no agent name matches '{role}'")
            continue
        c, _, e = run_mc(["agent", "skills", "add", aid, "--skill-ids", ",".join(ids)], dry_run)
        if c != 0:
            print(f"  bind fail  {role}: {e.strip()[:120]}", file=sys.stderr)
        else:
            print(f"  bind   {role} -> agent {aid[:8]} ({len(ids)} skill{'s' if len(ids) > 1 else ''})")


def main() -> int:
    ap = argparse.ArgumentParser(description="Register synced skills into multica.")
    ap.add_argument("--dry-run", action="store_true", help="Print multica commands without running")
    ap.add_argument("--profile", help="multica CLI profile (e.g. desktop-api.multica.ai to target the app's cloud)")
    ap.add_argument("--skill", help="Register only this skill name")
    ap.add_argument(
        "--bind", action=argparse.BooleanOptionalAction, default=True,
        help="Bind skills to matching role agent after register (default: on; use --no-bind)",
    )
    args = ap.parse_args()

    global _PROFILE
    _PROFILE = args.profile or ""

    reg = json.loads(REGISTRY.read_text(encoding="utf-8"))
    existing = {} if args.dry_run else list_existing()

    by_role: dict[str, list[str]] = {}
    failures = 0
    for entry in reg["skills"]:
        if args.skill and entry["name"] != args.skill:
            continue
        sid = upsert_skill(entry, existing, args.dry_run)
        if sid:
            entry["multica_id"] = sid
            by_role.setdefault(entry["role"], []).append(sid)
        else:
            failures += 1

    if not args.dry_run:
        REGISTRY.write_text(
            json.dumps(reg, indent=2, ensure_ascii=False) + "\n", encoding="utf-8"
        )

    print("\nbind hint (agent.skills) — 在 multica UI 把这些 skill id 绑到对应 role agent:")
    for role, ids in by_role.items():
        print(f"  {role}: {', '.join(ids) if ids else '(none)'}")
    if args.bind:
        print()
        bind_skills(reg, args.dry_run)
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
