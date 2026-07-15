#!/usr/bin/env python3
"""api_gate.py — Stage 5 API/interface verification gate.

A dedicated gate for the `api` command declared in a project's test-plan.json.
It is the script-first realization of the harness's stage-5 "API / interface
test" hard gate: run the project's api test command, block only on NEW api
failures (B − A), and leave unit/integration gating to baseline.py (which the
gate-runner tells to `--exclude api` so the two gates never overlap).

Why a separate gate (and not folded into baseline.py)?  baseline.py runs every
test-plan command in one snapshot and diffs them together.  Keeping api in its
own before/after pair lets stage 5 run AFTER stage 4's unit/integration gate
and carry its own evidence trail (baseline/api-{before,after,diff}.json),
mirroring the separation in the reference pipeline (Tencent 应用宝 phase 7
interface-verifier).

This is the *script* form of interface verification (does the api test command
pass, and did this change introduce new api failures).  The deeper form — a
dedicated interface-verifier agent that calls live endpoints and runs a
root-cause loop — is a documented future evolution, not implemented here.

Subcommands:

    snapshot --task <task> --phase before|after [--cwd <dir>] [--test-plan <p>]
        Run the `api` command from test-plan.json, write
        <task-root>/<task>/baseline/api-<phase>.json.
        Exit 0 also when NO api command is configured (prints SKIP); the
        gate-runner records stage 5 as skipped in that case.

    diff --task <task>
        Compute new/known/resolved api failures from api-before.json +
        api-after.json, write baseline/api-diff.json.
        Exit 0 = non-blocking, 1 = blocking (new api failures),
        2 = missing input (no api configured / snapshots absent).

test-plan.json schema (shared with baseline.py / detect_tests.py):

    { "api": { "cmd": "pnpm test:api", "hard": true } }

`cmd: null` or a missing `api` key means this project has no api test → skip.
"""
from __future__ import annotations

import argparse
import json
import sys
from datetime import datetime, timezone
from pathlib import Path

import baseline  # sibling import (same dir on sys.path[0]); reuse its primitives
import task_resolver

API_KEY = "api"


def _utc_now_iso() -> str:
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def _api_spec(cwd: Path, test_plan: str | None) -> tuple[str, str] | None:
    """Return (api_command_key, shell_cmd) from the project test-plan, or None.

    None means 'no api test configured for this project' — a legitimate skip.
    """
    plan_path = Path(test_plan) if test_plan else (cwd / "test-plan.json")
    loaded = baseline.load_test_plan(plan_path)  # (commands, templates) or None
    if not loaded:
        return None
    commands, templates = loaded
    if API_KEY not in commands:
        return None
    return API_KEY, templates[API_KEY]


def snapshot(
    *,
    task_dir: Path,
    phase: str,
    cwd: Path,
    spec: tuple[str, str],
    if_missing: bool = False,
) -> Path | None:
    """Run the api command, write baseline/api-<phase>.json. None = skipped."""
    key, shell_cmd = spec
    target = task_dir / "baseline" / f"api-{phase}.json"
    if if_missing and target.is_file():
        return target

    (task_dir / "baseline").mkdir(parents=True, exist_ok=True)
    exit_code, stdout, stderr, duration_ms = baseline.run_command(shell_cmd, cwd)
    failures = baseline.normalize_failures(API_KEY, stdout, stderr) if exit_code != 0 else []
    payload = {
        "schema": baseline.SCHEMA_VERSION,
        "task": task_dir.name,
        "ts": _utc_now_iso(),
        "commands": {
            key: {
                "command": shell_cmd,
                "exit_code": exit_code,
                "failures": failures,
                "duration_ms": duration_ms,
            }
        },
    }
    target.write_text(json.dumps(payload, ensure_ascii=False, indent=2), encoding="utf-8")
    return target


def diff(*, task_dir: Path) -> tuple[Path, dict]:
    """Read api-before/api-after, compute + write api-diff.json. Return (path, payload)."""
    base = task_dir / "baseline"
    before_path = base / "api-before.json"
    after_path = base / "api-after.json"
    if not before_path.exists():
        raise FileNotFoundError(
            f"missing baseline/api-before.json at {before_path}. "
            "Run `snapshot --phase before` first (or the project has no api test)."
        )
    if not after_path.exists():
        raise FileNotFoundError(
            f"missing baseline/api-after.json at {after_path}. Run `snapshot --phase after` first."
        )
    before = json.loads(before_path.read_text(encoding="utf-8"))
    after = json.loads(after_path.read_text(encoding="utf-8"))
    payload = baseline.compute_diff(before=before, after=after, task=task_dir.name)
    # compute_diff hardcodes the unit baseline filenames; relabel for api.
    payload["before"] = "baseline/api-before.json"
    payload["after"] = "baseline/api-after.json"
    target = base / "api-diff.json"
    target.write_text(json.dumps(payload, ensure_ascii=False, indent=2), encoding="utf-8")
    return target, payload


def cmd_snapshot(args: argparse.Namespace) -> int:
    try:
        task_dir = task_resolver.resolve_task_dir(args.task)
    except FileNotFoundError as e:
        print(f"error: {e}", file=sys.stderr)
        return 2
    cwd = Path(args.cwd).resolve() if args.cwd else Path.cwd()
    spec = _api_spec(cwd, args.test_plan)
    if spec is None:
        # Legitimate skip: no api command configured. Non-blocking.
        print("SKIP: no 'api' command in test-plan.json — stage 5 not applicable to this project.")
        return 0
    kept = args.if_missing and (task_dir / "baseline" / f"api-{args.phase}.json").is_file()
    try:
        target = snapshot(task_dir=task_dir, phase=args.phase, cwd=cwd, spec=spec, if_missing=args.if_missing)
    except Exception as e:
        print(f"error: {e}", file=sys.stderr)
        return 2
    if target is None:
        print("SKIP: no 'api' command in test-plan.json — stage 5 not applicable to this project.")
        return 0
    print(f"snapshot {'kept' if kept else 'written'} → {target}")
    return 0


def cmd_diff(args: argparse.Namespace) -> int:
    try:
        task_dir = task_resolver.resolve_task_dir(args.task)
    except FileNotFoundError as e:
        print(f"error: {e}", file=sys.stderr)
        return 2
    try:
        target, payload = diff(task_dir=task_dir)
    except FileNotFoundError as e:
        print(f"error: {e}", file=sys.stderr)
        return 2
    print(f"diff written → {target}")
    print(
        f"new_failures={len(payload['new_failures'])} "
        f"known_failures={len(payload['known_failures'])} "
        f"resolved_failures={len(payload['resolved_failures'])} "
        f"blocking={payload['blocking']}"
    )
    for f in payload["new_failures"]:
        print(f"  + {f}")
    return 1 if payload["blocking"] else 0


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="api_gate.py",
        description="Stage 5 API/interface verification gate (B−A on the test-plan 'api' command).",
    )
    sub = parser.add_subparsers(dest="command", required=True)

    p_snap = sub.add_parser("snapshot", help="Run the api command and capture failures.")
    p_snap.add_argument("--task", required=True, help="Task name, relative path, or absolute path.")
    p_snap.add_argument("--phase", required=True, choices=("before", "after"), help="Snapshot phase.")
    p_snap.add_argument("--test-plan", help="Path to test-plan.json (auto-detected at <cwd>/test-plan.json).")
    p_snap.add_argument("--cwd", help="Working directory to run the command in. Default: project root.")
    p_snap.add_argument("--if-missing", action="store_true", help="Keep an existing phase snapshot.")
    p_snap.set_defaults(func=cmd_snapshot)

    p_diff = sub.add_parser("diff", help="Compute diff from api-before/api-after snapshots.")
    p_diff.add_argument("--task", required=True, help="Task name, relative path, or absolute path.")
    p_diff.set_defaults(func=cmd_diff)
    return parser


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    return args.func(args)


if __name__ == "__main__":
    sys.exit(main())
