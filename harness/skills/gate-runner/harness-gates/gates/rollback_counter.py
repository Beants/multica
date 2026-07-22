#!/usr/bin/env python3
"""rollback_counter.py — Circuit breaker for consecutive rollbacks.

Tracks rollback count in task.json meta.rollbacks. When consecutive rollbacks
for the same phase reach the threshold (default 3), enforces a pause.

Usage:
    python3 scripts/rollback_counter.py --task <task-dir> --record --phase <phase>
        # Record a rollback event, check threshold

    python3 scripts/rollback_counter.py --task <task-dir> --status
        # Show current rollback status

    python3 scripts/rollback_counter.py --task <task-dir> --reset
        # Reset counter after user intervention

Records in task.json: meta.rollbacks = {"phase": "...", "count": N, "events": [...]}
"""
from __future__ import annotations

import argparse
import json
import sys
from datetime import datetime, timezone
from pathlib import Path

import task_resolver

import task_resolver
from task_resolver import evidence_dir as _evidence_dir

TASK_JSON = "task.json"
DEFAULT_THRESHOLD = 3


def _read_task_json(task_dir: Path) -> dict:
    p = _evidence_dir(task_dir) / TASK_JSON
    if not p.is_file():
        # Fallback: legacy location (workdir root) for backward compat
        p = task_dir / TASK_JSON
    if not p.is_file():
        return {}
    return json.loads(p.read_text(encoding="utf-8"))


def _write_task_json(task_dir: Path, data: dict) -> None:
    p = _evidence_dir(task_dir) / TASK_JSON
    if not p.is_file():
        # Fallback: legacy location
        p = task_dir / TASK_JSON
    p.write_text(json.dumps(data, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")


def main() -> int:
    parser = argparse.ArgumentParser(description="Circuit breaker for consecutive rollbacks")
    parser.add_argument("--task", required=True, help="Task name, relative path, or absolute path.")
    parser.add_argument("--record", action="store_true", help="Record a rollback event")
    parser.add_argument("--phase", help="Phase that rolled back (e.g. '2.1', '2.2')")
    parser.add_argument("--status", action="store_true", help="Show current status")
    parser.add_argument("--reset", action="store_true", help="Reset counter after intervention")
    parser.add_argument("--threshold", type=int, default=DEFAULT_THRESHOLD, help=f"Max rollbacks before breaker (default {DEFAULT_THRESHOLD})")
    args = parser.parse_args()

    try:
        task_dir = task_resolver.resolve_task_dir(args.task)
    except FileNotFoundError as e:
        print(f"Error: {e}", file=sys.stderr)
        return 1

    data = _read_task_json(task_dir)
    meta = data.setdefault("meta", {})
    rollbacks = meta.setdefault("rollbacks", {"phase": None, "count": 0, "events": []})

    if args.reset:
        meta["rollbacks"] = {"phase": None, "count": 0, "events": []}
        _write_task_json(task_dir, data)
        print("✓ Rollback counter reset.")
        return 0

    if args.status:
        phase = rollbacks.get("phase")
        count = rollbacks.get("count", 0)
        tripped = count >= args.threshold
        print(f"Phase: {phase or '(none)'}")
        print(f"Consecutive rollbacks: {count}")
        print(f"Threshold: {args.threshold}")
        print(f"Circuit breaker: {'TRIPPED ⛔' if tripped else 'OK ✓'}")
        if rollbacks.get("events"):
            print(f"Last event: {rollbacks['events'][-1]}")
        return 2 if tripped else 0

    if args.record:
        phase = args.phase or "unknown"

        # If same phase, increment; if different phase, reset and start new
        if rollbacks.get("phase") == phase:
            rollbacks["count"] = rollbacks.get("count", 0) + 1
        else:
            rollbacks["phase"] = phase
            rollbacks["count"] = 1
            rollbacks["events"] = []

        event_ts = datetime.now(timezone.utc).isoformat(timespec="seconds")
        rollbacks.setdefault("events", []).append(f"{event_ts} phase={phase}")

        _write_task_json(task_dir, data)

        count = rollbacks["count"]
        tripped = count >= args.threshold

        print(f"Rollback recorded: phase={phase}, count={count}")
        if tripped:
            print(f"⛔ CIRCUIT BREAKER TRIPPED: {count} consecutive rollbacks in phase {phase}")
            print("   PAUSE: ask user for guidance before retrying.")
            print("   Run with --reset after user intervention.")
            return 2
        else:
            remaining = args.threshold - count
            print(f"⚠ {remaining} more rollback(s) will trip the circuit breaker.")
            return 0

    parser.print_help()
    return 1


if __name__ == "__main__":
    sys.exit(main())
