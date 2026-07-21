#!/usr/bin/env python3
"""scar_summary.py — Soft-gate WARN scar aggregation.

Reads gate-result.jsonl, extracts all WARN/skipped events, and formats them
as a visible "scar" block for inclusion in check reports and spec reviews.

"你不能阻止 AI 偷懒，但你可以让偷懒变得很醒目。"

Usage:
    python3 scripts/scar_summary.py --task <task-dir>
        # Print scar summary

    python3 scripts/scar_summary.py --task <task-dir> --markdown
        # Output as markdown for pasting into task notes

    python3 scripts/scar_summary.py --task <task-dir> --count
        # Print only the count of scars (for CI integration)
"""
from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

import task_resolver


def _read_gates(task_dir: Path) -> list[dict]:
    gr = task_dir / "gate-result.jsonl"
    if not gr.is_file():
        return []
    events = []
    for line in gr.read_text(encoding="utf-8").splitlines():
        if not line.strip():
            continue
        try:
            events.append(json.loads(line))
        except json.JSONDecodeError:
            pass
    return events


def extract_scars(events: list[dict]) -> list[dict]:
    """Return events with status warn or skipped."""
    return [e for e in events if e.get("status") in ("warn", "skipped")]


def main() -> int:
    parser = argparse.ArgumentParser(description="Soft-gate WARN scar aggregation")
    parser.add_argument("--task", required=True, help="Task name, relative path, or absolute path.")
    parser.add_argument("--markdown", action="store_true", help="Output as markdown")
    parser.add_argument("--count", action="store_true", help="Print only scar count")
    args = parser.parse_args()

    try:
        task_dir = task_resolver.resolve_task_dir(args.task)
    except FileNotFoundError as e:
        print(f"Error: {e}", file=sys.stderr)
        return 1

    events = _read_gates(task_dir)
    scars = extract_scars(events)

    if args.count:
        print(len(scars))
        return 0

    if args.markdown:
        if not scars:
            print("## Soft-gate Scars\n\n_None this task._\n")
            return 0
        print("## Soft-gate Scars\n")
        print(f"_{len(scars)} warning(s) recorded — review before wrap-up._\n")
        print("| Phase | Gate | Command | Summary |")
        print("|---|---|---|---|")
        for s in scars:
            phase = s.get("phase", "?")
            gate = s.get("gate", "?")
            cmd = s.get("command", "")[:60]
            summary = s.get("summary", "")[:80]
            print(f"| {phase} | {gate} | `{cmd}` | {summary} |")
        print()
        return 0

    # Default text output
    if not scars:
        print("✓ No soft-gate scars.")
        return 0

    print("=" * 60)
    print(f"Soft-gate Scars ({len(scars)} warning(s))")
    print("=" * 60)
    for s in scars:
        phase = s.get("phase", "?")
        gate = s.get("gate", "?")
        status = s.get("status", "?")
        summary = s.get("summary", "")
        print(f"\n  [{status.upper()}] phase={phase} gate={gate}")
        if summary:
            print(f"    {summary}")
    print(f"\n{len(scars)} scar(s) — these are visible in check reports and delivery checklist.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
