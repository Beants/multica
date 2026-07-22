#!/usr/bin/env python3
"""gate_prd_confirm.py — Requirement confirmation gate.

Extracts a 5-line summary from prd.md and enforces user confirmation
before the workflow can proceed from Phase 1.1 to 1.2.

Usage:
    python3 scripts/gate_prd_confirm.py --task <task-dir>
        # Show summary, ask user to confirm interactively

    python3 scripts/gate_prd_confirm.py --task <task-dir> --confirm
        # Record confirmation and exit 0

    python3 scripts/gate_prd_confirm.py --task <task-dir> --status
        # Check if already confirmed, exit 0=confirmed 1=not confirmed

Records confirmation in task.json meta.prd_confirmed.
"""
from __future__ import annotations

import argparse
import json
import sys
from datetime import datetime, timezone
from pathlib import Path

import task_resolver
from task_resolver import evidence_dir as _evidence_dir, specs_dir as _specs_dir

TASK_JSON = "task.json"


def _read_task_json(task_dir: Path) -> dict:
    p = _evidence_dir(task_dir) / TASK_JSON
    if not p.is_file():
        p = task_dir / TASK_JSON  # fallback
    if not p.is_file():
        print(f"Error: {p} not found", file=sys.stderr)
        sys.exit(1)
    return json.loads(p.read_text(encoding="utf-8"))


def _write_task_json(task_dir: Path, data: dict) -> None:
    p = _evidence_dir(task_dir) / TASK_JSON
    if not p.is_file():
        p = task_dir / TASK_JSON  # fallback
    p.write_text(json.dumps(data, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")


def _extract_summary(prd_path: Path) -> list[str]:
    """Extract up to 5 key lines from prd.md."""
    if not prd_path.is_file():
        return ["(prd.md not found)"]
    text = prd_path.read_text(encoding="utf-8")
    lines = text.splitlines()
    summary: list[str] = []

    for line in lines:
        stripped = line.strip()
        if not stripped or stripped.startswith("#"):
            continue
        # Capture lines that look like requirement statements
        lower = stripped.lower()
        if any(kw in lower for kw in ("acceptance", "验收", "非目标", "non-goal", "scope", "范围")):
            summary.append(stripped[:120])
        elif len(summary) < 2 and not stripped.startswith("-"):
            # First couple of substantive content lines
            summary.append(stripped[:120])
        if len(summary) >= 5:
            break

    if not summary:
        # Fallback: first 5 non-empty, non-heading lines
        for line in lines:
            stripped = line.strip()
            if stripped and not stripped.startswith("#"):
                summary.append(stripped[:120])
                if len(summary) >= 5:
                    break

    return summary[:5] if summary else ["(no content found in prd.md)"]


def main() -> int:
    parser = argparse.ArgumentParser(description="Requirement confirmation gate")
    parser.add_argument("--task", required=True, help="Task name, relative path, or absolute path.")
    parser.add_argument("--confirm", action="store_true", help="Record confirmation")
    parser.add_argument("--status", action="store_true", help="Check confirmation status only")
    args = parser.parse_args()

    try:
        task_dir = task_resolver.resolve_task_dir(args.task)
    except FileNotFoundError as e:
        print(f"Error: {e}", file=sys.stderr)
        return 1

    data = _read_task_json(task_dir)
    meta = data.setdefault("meta", {})

    # Status check
    if args.status:
        if meta.get("prd_confirmed"):
            print(f"CONFIRMED at {meta['prd_confirmed']}")
            return 0
        print("NOT_CONFIRMED")
        return 1

    # Show summary
    prd_path = _specs_dir(task_dir) / "prd.md"
    if not prd_path.is_file():
        prd_path = task_dir / "prd.md"  # fallback
    summary = _extract_summary(prd_path)

    print("=" * 60)
    print("Requirement Confirmation Gate (Phase 1.1 → 1.2)")
    print("=" * 60)
    print(f"Task: {data.get('title', data.get('name', 'unknown'))}")
    print()
    print("5-line requirement summary:")
    for i, line in enumerate(summary, 1):
        print(f"  {i}. {line}")
    print()

    if meta.get("prd_confirmed"):
        print(f"✓ Already confirmed at {meta['prd_confirmed']}")
        return 0

    if args.confirm:
        meta["prd_confirmed"] = datetime.now(timezone.utc).isoformat(timespec="seconds")
        _write_task_json(task_dir, data)
        print("✓ Requirement confirmation recorded.")
        print("  Workflow may now proceed to Phase 1.2 (Research).")
        return 0

    print("NOT YET CONFIRMED. Run with --confirm after user approves the summary above.")
    print("  If user wants to modify → return to Phase 1.1 requirement exploration.")
    return 1


if __name__ == "__main__":
    sys.exit(main())
