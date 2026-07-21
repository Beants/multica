#!/usr/bin/env python3
"""plan_contract_check.py — Governance v0 advisory helper.

Checks a task directory's planning artifacts for contract quality:
- PRD has 6 required sections (problem / success / users / acceptance / non-goals / constraints)
- Acceptance criteria are observable (heuristic: line starts with "- [" and contains testable verb)
- design.md (if present) has contract / data flow / rollback sections

Advisory only — exit 0 always. Output JSON.
"""
from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path

import task_resolver

REQUIRED_PRD_SECTIONS = [
    ("problem", re.compile(r"^##\s+.*problem", re.IGNORECASE | re.MULTILINE)),
    ("success", re.compile(r"^##\s+.*(success|metric|goal)", re.IGNORECASE | re.MULTILINE)),
    ("users", re.compile(r"^##\s+.*(user|role|stakeholder)", re.IGNORECASE | re.MULTILINE)),
    ("acceptance", re.compile(r"^##\s+.*acceptance", re.IGNORECASE | re.MULTILINE)),
    ("non-goals", re.compile(r"^##\s+.*non.?goal", re.IGNORECASE | re.MULTILINE)),
    ("constraints", re.compile(r"^##\s+.*constraint", re.IGNORECASE | re.MULTILINE)),
]

DESIGN_REQUIRED_SECTIONS = [
    ("contract", re.compile(r"^##\s+.*contract", re.IGNORECASE | re.MULTILINE)),
    ("data flow", re.compile(r"^##\s+.*data.?flow", re.IGNORECASE | re.MULTILINE)),
    ("rollback", re.compile(r"^##\s+.*rollback|risk", re.IGNORECASE | re.MULTILINE)),
]

OBSERVABLE_AC_PATTERN = re.compile(r"^\s*-\s*\[[ x]\]\s+", re.MULTILINE)


def check_task(task_dir: Path) -> dict:
    prd_path = task_dir / "prd.md"
    design_path = task_dir / "design.md"
    implement_path = task_dir / "implement.md"

    passed = []
    warnings = []
    failed = []

    if not prd_path.exists():
        failed.append("prd.md missing")
        return {"schema": 1, "task": task_dir.name, "passed": passed, "warnings": warnings, "failed": failed}

    prd_text = prd_path.read_text(encoding="utf-8")

    for name, pattern in REQUIRED_PRD_SECTIONS:
        if pattern.search(prd_text):
            passed.append(f"PRD has section: {name}")
        else:
            warnings.append(f"PRD missing section: {name}")

    ac_matches = OBSERVABLE_AC_PATTERN.findall(prd_text)
    if len(ac_matches) >= 1:
        passed.append(f"PRD has {len(ac_matches)} acceptance criteria (observable checkbox format)")
    else:
        warnings.append("PRD has no observable acceptance criteria (expected '- [ ] or - [x] ...')")

    if design_path.exists():
        design_text = design_path.read_text(encoding="utf-8")
        for name, pattern in DESIGN_REQUIRED_SECTIONS:
            if pattern.search(design_text):
                passed.append(f"design.md has section: {name}")
            else:
                warnings.append(f"design.md missing section: {name}")
    else:
        warnings.append("design.md not present (acceptable for lightweight tasks)")

    if implement_path.exists():
        passed.append("implement.md present")
    else:
        warnings.append("implement.md not present (acceptable for lightweight tasks)")

    return {
        "schema": 1,
        "task": task_dir.name,
        "passed": passed,
        "warnings": warnings,
        "failed": failed,
    }


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Plan contract quality advisory check (governance v0)")
    parser.add_argument("--task", required=True, help="Task name, relative path, or absolute path.")
    args = parser.parse_args(argv)

    try:
        task_path = task_resolver.resolve_task_dir(args.task)
    except FileNotFoundError as e:
        print(json.dumps({"error": str(e)}, ensure_ascii=False))
        return 0

    result = check_task(task_path)
    print(json.dumps(result, ensure_ascii=False, indent=2))
    return 0


if __name__ == "__main__":
    sys.exit(main())
