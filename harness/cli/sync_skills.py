#!/usr/bin/env python3
"""sync_skills.py — 从外部 repo 同步 skill 到 harness/skills/<role>/<name>/。

不自编方法论 skill，复用社区验证过的（obra/superpowers 等）。保留 upstream
commit 锁定 + 新鲜度检查（对标应用宝 doc-staleness、Multica skills-lock.json）。

零第三方依赖（标准库 urllib + git ls-remote）。来源元数据分离写到 .source.json，
不污染上游 skill 原文（便于 diff/更新）。

用法:
    python3 harness/cli/sync_skills.py sync     # 按 registry.json 拉取并写入
    python3 harness/cli/sync_skills.py check    # 对比 pinned 与上游 HEAD，报告落后项

定时更新接入：用 Multica autopilot（schedule trigger，如每周）跑 `check`，
落后项写进 issue 通知；确认后跑 `sync` 升级。
"""
from __future__ import annotations

import argparse
import hashlib
import json
import subprocess
import sys
from datetime import datetime, timezone
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]  # harness/
REGISTRY = ROOT / "skills" / "registry.json"
SKILLS_DIR = ROOT / "skills"


def utc_now() -> str:
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def resolve_ref(repo: str, ref: str) -> str:
    """Return commit sha for repo's ref via git ls-remote. Fallback to HEAD."""
    url = f"https://github.com/{repo}.git"
    for candidate in (ref, "HEAD"):
        out = subprocess.run(
            ["git", "ls-remote", url, candidate],
            capture_output=True, text=True,
        )
        for line in out.stdout.splitlines():
            sha = line.split("\t", 1)[0]
            if sha:
                return sha
    raise RuntimeError(f"could not resolve ref {ref!r} @ {repo}")


def fetch_raw(repo: str, sha: str, path: str) -> bytes:
    """Fetch a single file via curl (uses system CA store; avoids Python's
    CERTIFICATE_VERIFY_FAILED on macOS when no certifi is installed)."""
    url = f"https://raw.githubusercontent.com/{repo}/{sha}/{path}"
    r = subprocess.run(["curl", "-fsSL", url], capture_output=True)
    if r.returncode != 0:
        raise RuntimeError(f"curl exit {r.returncode}: {r.stderr.decode(errors='replace').strip()[:200]}")
    return r.stdout


def sha256(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def do_sync() -> int:
    reg = json.loads(REGISTRY.read_text(encoding="utf-8"))
    failures = []
    for entry in reg["skills"]:
        name = entry["name"]
        repo = entry["repo"]
        role = entry["role"]
        if not repo:
            print(f"  skip   {role}/{name}: 自造 skill (repo=null)，不走社区同步")
            continue
        try:
            sha = resolve_ref(repo, entry.get("ref", "HEAD"))
            dest_dir = SKILLS_DIR / role / name
            dest_dir.mkdir(parents=True, exist_ok=True)
            files_meta = []
            for fpath in entry["files"]:
                data = fetch_raw(repo, sha, fpath)
                (dest_dir / Path(fpath).name).write_bytes(data)
                files_meta.append({"path": fpath, "hash": sha256(data)})
            # Source metadata stays separate so the upstream skill text is
            # byte-identical (clean diff on next sync).
            (dest_dir / ".source.json").write_text(
                json.dumps({
                    "repo": repo,
                    "ref": entry.get("ref", "HEAD"),
                    "commit": sha,
                    "files": files_meta,
                    "fetched_at": utc_now(),
                }, indent=2, ensure_ascii=False),
                encoding="utf-8",
            )
            entry["pinned"] = sha
            entry["last_synced"] = utc_now()
            print(f"  synced  {role}/{name} @ {sha[:8]}  ({len(entry['files'])} file)")
        except Exception as e:  # noqa: BLE001
            failures.append(f"{role}/{name}: {e}")
            print(f"  FAILED  {role}/{name}: {e}", file=sys.stderr)
    REGISTRY.write_text(
        json.dumps(reg, indent=2, ensure_ascii=False) + "\n", encoding="utf-8"
    )
    print(f"\nregistry updated → {REGISTRY.relative_to(ROOT)}")
    if failures:
        print(f"{len(failures)} failure(s).", file=sys.stderr)
    return 1 if failures else 0


def do_check() -> int:
    reg = json.loads(REGISTRY.read_text(encoding="utf-8"))
    stale: list[str] = []
    for entry in reg["skills"]:
        name = entry["name"]
        repo = entry["repo"]
        pinned = entry.get("pinned")
        label = f"{entry['role']}/{name}"
        if not repo:
            print(f"  skip    {label}: 自造 skill (repo=null)，无上游")
            continue
        if not pinned:
            stale.append(f"{label}: never synced")
            continue
        try:
            head = resolve_ref(repo, entry.get("ref", "HEAD"))
            if head != pinned:
                stale.append(f"{label}: pinned {pinned[:8]} <- upstream {head[:8]} (behind)")
            else:
                print(f"  ok      {label} @ {pinned[:8]}")
        except Exception as e:  # noqa: BLE001
            stale.append(f"{label}: check failed: {e}")
    if stale:
        print("\nstale / never-synced:")
        for s in stale:
            print(f"  - {s}")
        print(f"\n{len(stale)} skill(s) need sync. Run: python3 harness/cli/sync_skills.py sync")
        return 1
    print("\nall skills up to date.")
    return 0


def main() -> int:
    ap = argparse.ArgumentParser(description="Sync external skills into harness.")
    sub = ap.add_subparsers(dest="cmd", required=True)
    sub.add_parser("sync", help="Pull skills per registry.json")
    sub.add_parser("check", help="Compare pinned vs upstream HEAD")
    args = ap.parse_args()
    return do_sync() if args.cmd == "sync" else do_check()


if __name__ == "__main__":
    sys.exit(main())
