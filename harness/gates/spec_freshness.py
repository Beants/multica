#!/usr/bin/env python3
"""spec_freshness.py — Governance v0 advisory helper.

Checks .trellis/spec/ entries for freshness by comparing spec mtime
against recent task activity. Flags specs older than threshold (default
90 days) as "stale".

Advisory only — exit 0 always. Output JSON for dashboard consumption.
"""
from __future__ import annotations

import argparse
import json
import os
import sys
from datetime import datetime, timezone
from pathlib import Path

DEFAULT_THRESHOLD_DAYS = 90


def check_freshness(repo_root: Path, threshold_days: int = DEFAULT_THRESHOLD_DAYS) -> dict:
    spec_dir = repo_root / ".trellis" / "spec"
    tasks_dir = repo_root / ".trellis" / "tasks"

    if not spec_dir.exists():
        return {"schema": 1, "checked": 0, "stale": [], "fresh": [], "missing_dir": True}

    specs = find_spec_files(spec_dir)
    latest_task_activity = find_latest_task_activity(tasks_dir)
    threshold = datetime.now(timezone.utc).timestamp() - (threshold_days * 86400)

    stale = []
    fresh = []

    for spec_path in specs:
        try:
            mtime = spec_path.stat().st_mtime
        except OSError:
            continue

        rel_path = str(spec_path.relative_to(repo_root))
        entry = {
            "path": rel_path,
            "mtime": datetime.fromtimestamp(mtime, timezone.utc).strftime("%Y-%m-%d"),
            "age_days": int((datetime.now(timezone.utc).timestamp() - mtime) / 86400),
        }

        if mtime < threshold and mtime < latest_task_activity:
            entry["reason"] = f"spec older than {threshold_days} days and predates latest task activity"
            stale.append(entry)
        else:
            fresh.append(entry)

    return {
        "schema": 1,
        "checked": len(specs),
        "stale": stale,
        "fresh": fresh,
        "missing_dir": False,
        "threshold_days": threshold_days,
    }


def find_spec_files(spec_dir: Path) -> list[Path]:
    return sorted(spec_dir.rglob("*.md"))


def find_latest_task_activity(tasks_dir: Path) -> float:
    if not tasks_dir.exists():
        return 0.0
    latest = 0.0
    for f in tasks_dir.rglob("*"):
        if f.is_file():
            try:
                latest = max(latest, f.stat().st_mtime)
            except OSError:
                continue
    return latest


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Spec freshness advisory check (governance v0)")
    parser.add_argument("--root", default=".", help="Repository root (default: CWD)")
    parser.add_argument("--threshold", type=int, default=DEFAULT_THRESHOLD_DAYS, help="Stale threshold in days")
    args = parser.parse_args(argv)

    result = check_freshness(Path(args.root).resolve(), args.threshold)
    print(json.dumps(result, ensure_ascii=False, indent=2))
    return 0


if __name__ == "__main__":
    sys.exit(main())
