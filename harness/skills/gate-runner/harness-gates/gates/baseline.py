#!/usr/bin/env python3
"""baseline.py — Evidence Runtime v0 helper.

Two subcommands:

    snapshot --task <task> --phase before|after
        Run lint/typecheck/test (or a subset), capture exit_code + normalized
        failures, write <task-root>/<task>/baseline/<phase>.json.

    diff --task <task>
        Compute new / known / resolved failure sets from before.json + after.json,
        write <task-root>/<task>/baseline/diff.json. Print summary.
        Exit 0 = non-blocking, 1 = blocking (new failures), 2 = missing input.

Schema reference: the companion spec guide
`baseline-and-gate-result-protocol.md`.
"""
from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
from datetime import datetime, timezone
from pathlib import Path

import task_resolver
from task_resolver import baseline_dir as _baseline_dir

SCHEMA_VERSION = 1
DEFAULT_COMMANDS = ("lint", "typecheck", "test")
UNKNOWN_LINE_BUDGET = 200

# Default commands run per project layout. Users override via --commands.
DEFAULT_COMMAND_TEMPLATES = {
    "lint": "pnpm lint",
    "typecheck": "pnpm typecheck",
    "test": "pnpm test",
}

# Failure normalization patterns. Best-effort: unknown formats fall back to
# the first 200 chars of any line matching /error|fail|✗/i.
NORMALIZERS = {
    "lint": [
        re.compile(
            r"^(?P<file>.+?):(?P<line>\d+):(?P<col>\d+):\s+(?P<msg>.+?)\s+\["
        ),
    ],
    "typecheck": [
        re.compile(
            r"^(?P<file>.+?):(?P<line>\d+):(?P<col>\d+)\s*-\s*error\s+(?P<code>TS\d+)\s+(?P<msg>.+)$"
        ),
    ],
    "test": [
        re.compile(r"^\s*FAIL\s+(?P<file>.+?)\s+>\s+(?P<name>.+)$"),
        re.compile(r"^\s*✕\s+(?P<name>.+?)\s+\("),
    ],
}
UNKNOWN_FALLBACK = re.compile(r"error|fail|✗", re.IGNORECASE)


def _utc_now_iso() -> str:
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def normalize_failures(command_key: str, stdout: str, stderr: str) -> list[str]:
    """Return a list of normalized failure signatures for the given command key.

    Best-effort. Unknown formats fall back to first 200 chars of any line
    matching /error|fail|✗/i.
    """
    patterns = NORMALIZERS.get(command_key, [])
    out: list[str] = []
    seen: set[str] = set()
    blob = (stdout or "") + ("\n" if stdout and stderr else "") + (stderr or "")
    if not blob:
        return out

    for line in blob.splitlines():
        if not line.strip():
            continue
        matched = False
        for pat in patterns:
            m = pat.search(line)
            if not m:
                continue
            d = m.groupdict()
            # Compose a stable signature.
            if "file" in d and "line" in d:
                sig = f"{d['file']}:{d['line']}"
                if "col" in d and d.get("col"):
                    sig += f":{d['col']}"
                if "msg" in d and d["msg"]:
                    sig += f" - {d['msg']}"
                elif "code" in d and d.get("code"):
                    sig += f" - {d['code']}"
            elif "name" in d:
                sig = f"FAIL > {d['name']}"
            else:
                continue
            if sig not in seen:
                seen.add(sig)
                out.append(sig)
            matched = True
            break
        if matched:
            continue
        # Fallback: line matches /error|fail|✗/i; cap at 200 chars.
        if UNKNOWN_FALLBACK.search(line):
            sig = line.strip()[:UNKNOWN_LINE_BUDGET]
            if sig not in seen:
                seen.add(sig)
                out.append(sig)
    return out


def run_command(shell_cmd: str, cwd: Path) -> tuple[int, str, str, int]:
    """Run shell_cmd in cwd; return (exit_code, stdout, stderr, duration_ms)."""
    start_ts = datetime.now(timezone.utc)
    try:
        proc = subprocess.run(
            shell_cmd,
            shell=True,
            cwd=str(cwd),
            capture_output=True,
            text=True,
            timeout=600,
        )
        stdout = proc.stdout or ""
        stderr = proc.stderr or ""
        exit_code = proc.returncode
    except subprocess.TimeoutExpired as e:
        stdout = (e.stdout or "") if isinstance(e.stdout, str) else ""
        stderr = (e.stderr or "") if isinstance(e.stderr, str) else ""
        stderr = (stderr + "\n[baseline.py timeout after 600s]").strip()
        exit_code = 124
    duration_ms = int((datetime.now(timezone.utc) - start_ts).total_seconds() * 1000)
    return exit_code, stdout, stderr, duration_ms


def snapshot(
    *,
    task_dir: Path,
    phase: str,
    commands: list[str],
    cwd: Path,
    if_missing: bool = False,
    command_templates: dict[str, str] | None = None,
) -> Path:
    """Run each command, capture results, write <task>/.harness/evidence/baseline/<phase>.json."""
    task_name = task_dir.name
    bdir = _baseline_dir(task_dir)
    target = bdir / f"{phase}.json"
    if if_missing and target.is_file():
        return target

    bdir.mkdir(parents=True, exist_ok=True)

    cmd_results: dict[str, dict] = {}
    for key in commands:
        shell_cmd = (
            command_templates[key]
            if command_templates is not None
            else DEFAULT_COMMAND_TEMPLATES.get(key, key)
        )
        exit_code, stdout, stderr, duration_ms = run_command(shell_cmd, cwd)
        failures = normalize_failures(key, stdout, stderr) if exit_code != 0 else []
        cmd_results[key] = {
            "command": shell_cmd,
            "exit_code": exit_code,
            "failures": failures,
            "duration_ms": duration_ms,
        }

    payload = {
        "schema": SCHEMA_VERSION,
        "task": task_name,
        "ts": _utc_now_iso(),
        "commands": cmd_results,
    }
    target.write_text(json.dumps(payload, ensure_ascii=False, indent=2), encoding="utf-8")
    return target


def compute_diff(
    *,
    before: dict,
    after: dict,
    task: str,
) -> dict:
    """Compute new/known/resolved failures per command. Return diff payload.

    Failure signatures are prefixed with `[<command_key>] ` so dashboard can
    group by command source. Within a command, signatures are sorted for
    deterministic output.
    """
    new_failures: list[str] = []
    known_failures: list[str] = []
    resolved_failures: list[str] = []

    before_cmds = before.get("commands", {})
    after_cmds = after.get("commands", {})
    # Preserve first-seen order across before then after; dedupe.
    all_keys: list[str] = []
    seen_keys: set[str] = set()
    for k in [*before_cmds.keys(), *after_cmds.keys()]:
        if k not in seen_keys:
            all_keys.append(k)
            seen_keys.add(k)

    for key in all_keys:
        b = set(before_cmds.get(key, {}).get("failures", []))
        a = set(after_cmds.get(key, {}).get("failures", []))
        for f in sorted(a - b):
            new_failures.append(f"[{key}] {f}")
        for f in sorted(a & b):
            known_failures.append(f"[{key}] {f}")
        for f in sorted(b - a):
            resolved_failures.append(f"[{key}] {f}")

    return {
        "schema": SCHEMA_VERSION,
        "task": task,
        "before": ".harness/evidence/baseline/before.json",
        "after": ".harness/evidence/baseline/after.json",
        "new_failures": new_failures,
        "known_failures": known_failures,
        "resolved_failures": resolved_failures,
        "blocking": len(new_failures) > 0,
    }


def diff(
    *,
    task_dir: Path,
) -> tuple[Path, dict]:
    """Read .harness/evidence/baseline/{before,after}.json, compute + write diff.json. Return (path, payload)."""
    task_name = task_dir.name
    base = _baseline_dir(task_dir)
    before_path = base / "before.json"
    after_path = base / "after.json"
    if not before_path.exists():
        raise FileNotFoundError(
            f"missing .harness/evidence/baseline/before.json at {before_path}. Run `snapshot --phase before` first."
        )
    if not after_path.exists():
        raise FileNotFoundError(
            f"missing .harness/evidence/baseline/after.json at {after_path}. Run `snapshot --phase after` first."
        )

    before = json.loads(before_path.read_text(encoding="utf-8"))
    after = json.loads(after_path.read_text(encoding="utf-8"))
    payload = compute_diff(before=before, after=after, task=task_name)
    target = base / "diff.json"
    target.write_text(json.dumps(payload, ensure_ascii=False, indent=2), encoding="utf-8")
    return target, payload


def load_test_plan(path: Path) -> tuple[list[str], dict[str, str]] | None:
    """Load a project test-plan.json: {<key>: {"cmd": "...", "hard": bool}, ...}.

    Returns (commands, command_templates), or None when absent/unreadable. Lets
    baseline run the project's real test stack (e.g. `make test` for Go +
    `pnpm test` for TS) instead of the hardcoded pnpm default. Entries with
    cmd:null are skipped (that test type isn't configured for this project).
    """
    if not path.is_file():
        return None
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as e:
        print(f"warning: test-plan {path} unreadable ({e}); falling back", file=sys.stderr)
        return None
    commands: list[str] = []
    templates: dict[str, str] = {}
    for key, spec in data.items():
        if not isinstance(spec, dict):
            continue
        cmd = spec.get("cmd")
        if not cmd:
            continue
        commands.append(key)
        templates[key] = cmd
    return (commands, templates) if commands else None


def cmd_snapshot(args: argparse.Namespace) -> int:
    try:
        task_dir = task_resolver.resolve_task_dir(args.task)
    except FileNotFoundError as e:
        print(f"error: {e}", file=sys.stderr)
        return 2

    cwd = Path(args.cwd).resolve() if args.cwd else Path.cwd()
    command_templates: dict[str, str] | None = None

    # Project test-plan wins (real test stack), then --commands, then pnpm default.
    # Check .harness/test-plan.json first, fallback to workdir root.
    from task_resolver import test_plan_path as _test_plan_path
    if args.test_plan:
        plan_path = Path(args.test_plan)
    else:
        plan_path = _test_plan_path(cwd)
        if not plan_path.is_file():
            plan_path = cwd / "test-plan.json"  # fallback: legacy location
    tp = load_test_plan(plan_path)
    if tp:
        commands, command_templates = tp
    elif args.commands:
        commands = [c.strip() for c in args.commands.split(",") if c.strip()]
    else:
        commands = list(DEFAULT_COMMANDS)
        if not plan_path.is_file():
            print(
                f"warning: no test-plan.json at {plan_path}; using pnpm default — "
                "you may miss project tests (e.g. `make test` for Go). "
                "Add one or run gates/detect_tests.py.",
                file=sys.stderr,
            )

    # Drop keys gated elsewhere (e.g. 'api' is gated by api_gate.py at stage 5,
    # so the baseline diff at stage 4 must not also run/re diffs it).
    if args.exclude:
        excluded = {c.strip() for c in args.exclude.split(",") if c.strip()}
        if excluded:
            commands = [c for c in commands if c not in excluded]
            if command_templates is not None:
                command_templates = {
                    k: v for k, v in command_templates.items() if k not in excluded
                }
            if not commands:
                print("error: --exclude removed every command", file=sys.stderr)
                return 2
    target_before = _baseline_dir(task_dir) / f"{args.phase}.json"
    kept_existing = args.if_missing and target_before.is_file()
    try:
        target = snapshot(
            task_dir=task_dir,
            phase=args.phase,
            commands=commands,
            cwd=cwd,
            if_missing=args.if_missing,
            command_templates=command_templates,
        )
    except Exception as e:
        print(f"error: {e}", file=sys.stderr)
        return 2
    action = "kept" if kept_existing else "written"
    print(f"snapshot {action} → {target}")
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

    # Print summary
    print(f"diff written → {target}")
    print(
        f"new_failures={len(payload['new_failures'])} "
        f"known_failures={len(payload['known_failures'])} "
        f"resolved_failures={len(payload['resolved_failures'])} "
        f"blocking={payload['blocking']}"
    )
    if payload["new_failures"]:
        print("\nNew failures:")
        for f in payload["new_failures"]:
            print(f"  + {f}")
    if payload["resolved_failures"]:
        print("\nResolved failures:")
        for f in payload["resolved_failures"]:
            print(f"  - {f}")
    return 1 if payload["blocking"] else 0


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="baseline.py",
        description="Evidence Runtime v0 baseline snapshot/diff helper.",
    )
    sub = parser.add_subparsers(dest="command", required=True)

    p_snap = sub.add_parser("snapshot", help="Run commands and capture failures.")
    p_snap.add_argument("--task", required=True, help="Task name, relative path, or absolute path.")
    p_snap.add_argument(
        "--phase", required=True, choices=("before", "after"), help="Snapshot phase."
    )
    p_snap.add_argument(
        "--task-root",
        help=argparse.SUPPRESS,
    )
    p_snap.add_argument(
        "--commands",
        help=f"Comma-separated command keys. Default: {','.join(DEFAULT_COMMANDS)}.",
    )
    p_snap.add_argument(
        "--test-plan",
        help="Path to a project test-plan.json (auto-detected at <cwd>/test-plan.json if omitted).",
    )
    p_snap.add_argument(
        "--exclude",
        help="Comma-separated command keys to skip (e.g. 'api' when api is gated separately by api_gate.py).",
    )
    p_snap.add_argument(
        "--cwd", help="Working directory to run commands in. Default: project root."
    )
    p_snap.add_argument(
        "--if-missing",
        action="store_true",
        help="Keep an existing phase snapshot and skip command execution.",
    )
    p_snap.set_defaults(func=cmd_snapshot)

    p_diff = sub.add_parser("diff", help="Compute diff from before/after snapshots.")
    p_diff.add_argument("--task", required=True, help="Task name, relative path, or absolute path.")
    p_diff.add_argument(
        "--task-root",
        help=argparse.SUPPRESS,
    )
    p_diff.set_defaults(func=cmd_diff)
    return parser


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    return args.func(args)


if __name__ == "__main__":
    sys.exit(main())
