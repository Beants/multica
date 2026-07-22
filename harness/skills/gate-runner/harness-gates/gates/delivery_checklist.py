#!/usr/bin/env python3
"""delivery_checklist.py — Delivery completion checklist.

Checks whether all required deliverables are present before Phase 3.5 wrap-up.
Reports missing items; exit 0=ready, 1=not ready, 2=no active task.

Usage:
    python3 scripts/delivery_checklist.py --task <task-dir>
    python3 scripts/delivery_checklist.py --task <task-dir> --json
"""
from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

import task_resolver
from task_resolver import specs_dir as _specs_dir, evidence_dir as _evidence_dir, baseline_dir as _baseline_dir

TASK_JSON = "task.json"


def _read_task_json(task_dir: Path) -> dict:
    p = _evidence_dir(task_dir) / TASK_JSON
    if not p.is_file():
        p = task_dir / TASK_JSON  # fallback: legacy location
    if not p.is_file():
        return {}
    return json.loads(p.read_text(encoding="utf-8"))


def _read_json_object(path: Path, label: str, missing: list[str]) -> dict | None:
    if not path.is_file():
        missing.append(f"{label} missing")
        return None
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        missing.append(f"{label} unreadable: {exc}")
        return None
    if not isinstance(value, dict):
        missing.append(f"{label} must contain a JSON object")
        return None
    return value


def _read_gate_events(path: Path, warnings: list[str]) -> list[dict]:
    if not path.is_file():
        return []
    events: list[dict] = []
    for line_number, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
        if not line.strip():
            continue
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            warnings.append(f"gate-result.jsonl line {line_number} is malformed")
            continue
        if isinstance(event, dict):
            events.append(event)
        else:
            warnings.append(f"gate-result.jsonl line {line_number} is not an object")
    return events


def _command_contract(snapshot: dict) -> dict[str, object] | None:
    commands = snapshot.get("commands")
    if not isinstance(commands, dict):
        return None
    contract: dict[str, object] = {}
    for command_id, result in commands.items():
        if not isinstance(command_id, str) or not isinstance(result, dict):
            return None
        contract[command_id] = result.get("command")
    return contract


def check_task(task_dir: Path) -> dict:
    """Return {"passed": [...], "missing": [...], "warnings": [...]}."""
    data = _read_task_json(task_dir)
    sdir = _specs_dir(task_dir)
    is_complex = (sdir / "design.md").is_file() or (task_dir / "design.md").is_file()
    passed: list[str] = []
    missing: list[str] = []
    warnings: list[str] = []

    # Required for all tasks
    if (sdir / "prd.md").is_file() or (task_dir / "prd.md").is_file():
        passed.append("prd.md exists")
    else:
        missing.append("prd.md missing")

    # Required for complex tasks
    if is_complex:
        for f in ("design.md", "implement.md"):
            if (sdir / f).is_file() or (task_dir / f).is_file():
                passed.append(f"{f} exists")
            else:
                missing.append(f"{f} missing (complex task)")

    # implement.jsonl / check.jsonl should have real entries
    for jf in ("implement.jsonl", "check.jsonl"):
        path = _evidence_dir(task_dir) / jf
        if not path.is_file():
            path = task_dir / jf  # fallback
        if path.is_file():
            lines = [
                line
                for line in path.read_text().splitlines()
                if line.strip() and '"_example"' not in line
            ]
            if lines:
                passed.append(f"{jf} has {len(lines)} real entries")
            else:
                warnings.append(f"{jf} exists but has no real entries (only _example)")
        else:
            warnings.append(f"{jf} not found")

    # Evidence runtime
    gr = _evidence_dir(task_dir) / "gate-result.jsonl"
    if not gr.is_file():
        gr = task_dir / "gate-result.jsonl"  # fallback
    events = _read_gate_events(gr, warnings)
    if gr.is_file():
        count = len(events)
        passed.append(f"gate-result.jsonl has {count} events")
        # Check for any fail status
        fails = 0
        warns = 0
        for evt in events:
            if evt.get("status") == "fail" and evt.get("hard"):
                fails += 1
            elif evt.get("status") == "warn":
                warns += 1
        if fails > 0:
            missing.append(f"{fails} hard-gate failures in gate-result.jsonl")
        if warns > 0:
            warnings.append(f"{warns} soft-gate warnings (scars) — review before wrap-up")
    else:
        warnings.append("gate-result.jsonl not found (no evidence trail)")

    # Baseline
    baseline_diff = _baseline_dir(task_dir) / "diff.json"
    if not baseline_diff.is_file():
        baseline_diff = task_dir / "baseline" / "diff.json"  # fallback
    if baseline_diff.is_file():
        try:
            diff = json.loads(baseline_diff.read_text(encoding="utf-8"))
            new_fails = len(diff.get("new_failures", []))
            if new_fails > 0:
                missing.append(f"baseline diff shows {new_fails} new failures")
            else:
                passed.append("baseline diff clean (no new failures)")
        except json.JSONDecodeError:
            warnings.append("baseline/diff.json malformed")
    else:
        warnings.append("baseline/diff.json not found")

    # Optional traceability becomes a hard delivery contract when selected.
    verification_path = _specs_dir(task_dir) / "verification-contract.json"
    if not verification_path.is_file():
        verification_path = task_dir / "verification-contract.json"  # fallback
    if verification_path.is_file():
        try:
            import verification_contract_check
        except ImportError:
            missing.append(
                "verification contract selected but verification_contract_check.py is unavailable"
            )
        else:
            results_path = _evidence_dir(task_dir) / "case-results.jsonl"
            if not results_path.is_file():
                results_path = task_dir / "case-results.jsonl"  # fallback
            try:
                contract = json.loads(verification_path.read_text(encoding="utf-8"))
                if not isinstance(contract, dict):
                    raise verification_contract_check.ContractInputError(
                        "contract root must be a JSON object"
                    )
                result_lines = (
                    results_path.read_text(encoding="utf-8").splitlines()
                    if results_path.is_file()
                    else []
                )
                case_results = verification_contract_check.parse_results(result_lines)
                verification = verification_contract_check.check_contract(
                    contract,
                    case_results,
                    require_frozen=True,
                    require_results=True,
                )
            except (
                OSError,
                json.JSONDecodeError,
                verification_contract_check.ContractInputError,
            ) as exc:
                missing.append(f"verification contract unreadable: {exc}")
            else:
                if verification["issues"]:
                    missing.extend(
                        f"verification contract: {issue}"
                        for issue in verification["issues"]
                    )
                else:
                    passed.append(
                        "verification contract frozen with complete current results"
                    )

    # Task status should be in_progress (not planning)
    status = data.get("status", "unknown")
    if status == "completed":
        warnings.append("task already completed")
    elif status != "in_progress":
        warnings.append(f"task status is '{status}', expected 'in_progress'")

    # Branch info
    if data.get("branch"):
        passed.append(f"branch set: {data['branch']}")
    else:
        warnings.append("no git branch recorded in task.json")

    # Research artifacts (recommended for complex)
    research_dir = _specs_dir(task_dir) / "research"
    if not research_dir.is_dir():
        research_dir = task_dir / "research"  # fallback
    if research_dir.is_dir():
        files = list(research_dir.glob("*.md"))
        if files:
            passed.append(f"research/ has {len(files)} files")
        else:
            warnings.append("research/ dir exists but empty")
    elif is_complex:
        warnings.append("research/ not found (recommended for complex tasks)")

    return {"passed": passed, "missing": missing, "warnings": warnings}


def main() -> int:
    parser = argparse.ArgumentParser(description="Delivery completion checklist")
    parser.add_argument("--task", required=True, help="Task name, relative path, or absolute path.")
    parser.add_argument("--json", action="store_true", help="Output JSON")
    args = parser.parse_args()

    try:
        task_dir = task_resolver.resolve_task_dir(args.task)
    except FileNotFoundError as e:
        print(f"Error: {e}", file=sys.stderr)
        return 2

    result = check_task(task_dir)

    if args.json:
        print(json.dumps(result, indent=2, ensure_ascii=False))
    else:
        print("=" * 60)
        print("Delivery Checklist")
        print("=" * 60)
        if result["passed"]:
            print("\n✓ Passed:")
            for item in result["passed"]:
                print(f"  ✓ {item}")
        if result["missing"]:
            print("\n✗ Missing (blocks delivery):")
            for item in result["missing"]:
                print(f"  ✗ {item}")
        if result["warnings"]:
            print("\n⚠ Warnings (review before wrap-up):")
            for item in result["warnings"]:
                print(f"  ⚠ {item}")

        if result["missing"]:
            print(f"\nNOT READY: {len(result['missing'])} blocking issue(s)")
        else:
            print("\nREADY: all checks passed")

    return 1 if result["missing"] else 0


if __name__ == "__main__":
    sys.exit(main())
