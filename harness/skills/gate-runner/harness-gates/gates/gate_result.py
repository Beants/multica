#!/usr/bin/env python3
"""gate_result.py — Evidence Runtime v0 helper.

Appends one JSON line to <task-root>/<task>/gate-result.jsonl per call.
Validates enum values; exits 2 on invalid enum. Never overwrites existing
lines (append-only).

Schema reference: the companion spec guide
`baseline-and-gate-result-protocol.md`.
"""
from __future__ import annotations

import argparse
import json
import sys
from datetime import datetime, timezone
from pathlib import Path
from typing import Iterable

import task_resolver
from task_resolver import evidence_dir as _evidence_dir

SCHEMA_VERSION = 1

VALID_PHASES = ("plan", "implement", "check", "finish")
VALID_GATES = (
    "prd",
    "design",
    "baseline",
    "lint",
    "typecheck",
    "test",
    "self-review",
    "soft-gate",
)
VALID_STATUSES = ("pass", "fail", "warn", "skipped")

DEFAULT_HARD_BY_GATE = {
    "prd": False,
    "design": False,
    "baseline": True,
    "lint": True,
    "typecheck": True,
    "test": True,
    "self-review": True,
    "soft-gate": False,
}


class EnumValidationError(ValueError):
    """Raised when an enum value is invalid."""


def _normalize_hard(gate: str, hard_flag: str | None) -> bool:
    """Resolve `--hard` / `--soft` against the gate default.

    `None` (neither flag passed) → use gate default.
    Explicit flag wins over default.
    """
    if hard_flag == "hard":
        return True
    if hard_flag == "soft":
        return False
    return DEFAULT_HARD_BY_GATE[gate]


def _utc_now_iso() -> str:
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def build_event(
    *,
    task: str,
    phase: str,
    gate: str,
    status: str,
    command: str,
    duration_ms: int,
    hard: str | None,
    summary: str,
    evidence: Iterable[str] | None,
    new_failures: int,
) -> dict:
    """Validate inputs and build the event dict. Raises EnumValidationError on bad enum."""
    if phase not in VALID_PHASES:
        raise EnumValidationError(
            f"invalid phase {phase!r}; expected one of {VALID_PHASES}"
        )
    if gate not in VALID_GATES:
        raise EnumValidationError(
            f"invalid gate {gate!r}; expected one of {VALID_GATES}"
        )
    if status not in VALID_STATUSES:
        raise EnumValidationError(
            f"invalid status {status!r}; expected one of {VALID_STATUSES}"
        )
    if duration_ms < 0:
        raise EnumValidationError(
            f"duration_ms must be >= 0, got {duration_ms}"
        )
    if new_failures < 0:
        raise EnumValidationError(
            f"new_failures must be >= 0, got {new_failures}"
        )

    resolved_hard = _normalize_hard(gate, hard)
    return {
        "schema": SCHEMA_VERSION,
        "ts": _utc_now_iso(),
        "task": task,
        "phase": phase,
        "gate": gate,
        "command": command,
        "status": status,
        "duration_ms": duration_ms,
        "hard": resolved_hard,
        "summary": summary,
        "evidence": list(evidence) if evidence else [],
        "new_failures": new_failures,
    }


def append_event(
    *,
    task_dir: Path,
    event: dict,
) -> Path:
    """Append `event` as one JSON line to <task-dir>/gate-result.jsonl.

    Creates the file (and parent directory) if missing. Never overwrites existing
    content. Returns the path written to.
    """
    task_dir.mkdir(parents=True, exist_ok=True)
    target = _evidence_dir(task_dir) / "gate-result.jsonl"
    line = json.dumps(event, ensure_ascii=False, separators=(",", ":"))
    # Append mode: never truncate.
    with target.open("a", encoding="utf-8") as f:
        f.write(line + "\n")
    return target


def _parse_evidence(raw: str | None) -> list[str]:
    if not raw:
        return []
    return [piece.strip() for piece in raw.split(",") if piece.strip()]


def cmd_append(args: argparse.Namespace) -> int:
    try:
        task_dir = task_resolver.resolve_task_dir(args.task)
    except FileNotFoundError as e:
        print(f"error: {e}", file=sys.stderr)
        return 2

    # Mutually exclusive group ensures hard_flag is "hard", "soft", or None.
    hard_flag: str | None = None
    if args.hard:
        hard_flag = "hard"
    elif args.soft:
        hard_flag = "soft"

    try:
        event = build_event(
            task=task_dir.name,
            phase=args.phase,
            gate=args.gate,
            status=args.status,
            command=args.command,
            duration_ms=args.duration_ms,
            hard=hard_flag,
            summary=args.summary,
            evidence=_parse_evidence(args.evidence),
            new_failures=args.new_failures,
        )
    except EnumValidationError as e:
        print(f"error: {e}", file=sys.stderr)
        return 2

    written = append_event(task_dir=task_dir, event=event)
    print(f"appended → {written}")
    return 0


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="gate_result.py",
        description=(
            "Evidence Runtime v0 helper. Appends one JSON line to "
            "<task-root>/<task>/gate-result.jsonl per call."
        ),
    )
    sub = parser.add_subparsers(dest="command", required=True)

    p_append = sub.add_parser(
        "append", help="Append one gate-result.jsonl event."
    )
    p_append.add_argument("--task", required=True, help="Task name, relative path, or absolute path.")
    p_append.add_argument(
        "--phase", required=True, choices=VALID_PHASES, help="Plan/Implement/Check/Finish"
    )
    p_append.add_argument(
        "--gate", required=True, choices=VALID_GATES, help="Gate kind."
    )
    p_append.add_argument(
        "--status", required=True, choices=VALID_STATUSES, help="Outcome status."
    )
    p_append.add_argument(
        "--command", required=True, help="Shell command that was run (empty string allowed)."
    )
    p_append.add_argument(
        "--duration-ms",
        required=True,
        type=int,
        help="Wall-clock duration in milliseconds. Use 0 for non-command gates.",
    )
    hard_group = p_append.add_mutually_exclusive_group()
    hard_group.add_argument(
        "--hard",
        action="store_true",
        help="Mark as hard gate (can block). Overrides gate default.",
    )
    hard_group.add_argument(
        "--soft",
        action="store_true",
        help="Mark as soft gate (scar only). Overrides gate default.",
    )
    p_append.add_argument(
        "--summary", required=True, help="One-line result summary."
    )
    p_append.add_argument(
        "--evidence",
        help="Comma-separated relative paths or source IDs.",
    )
    p_append.add_argument(
        "--new-failures",
        type=int,
        default=0,
        help="Count from baseline diff. Default 0.",
    )
    p_append.add_argument(
        "--task-root",
        help=argparse.SUPPRESS,
    )
    p_append.set_defaults(func=cmd_append)
    return parser


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    return args.func(args)


if __name__ == "__main__":
    sys.exit(main())
