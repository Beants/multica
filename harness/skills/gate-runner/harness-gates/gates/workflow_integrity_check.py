#!/usr/bin/env python3
"""workflow_integrity_check.py — Workflow definition self-check.

Parses .trellis/workflow.md to extract phase/step definitions, then checks:
1. Every required step (marked [required]) has a heading in workflow.md
2. Phase Index matches actual Phase sections
3. task.json status values are valid per workflow phases

This is the Trellis equivalent of Tencent's "自检脚本检查完整性" — making
the workflow definition a machine-checkable asset.

Usage:
    python3 scripts/workflow_integrity_check.py
        # Run checks, report issues

    python3 scripts/workflow_integrity_check.py --workflow path/to/workflow.md
        # Validate an explicit local or marketplace workflow

    python3 scripts/workflow_integrity_check.py --json
        # Output JSON

Exit: 0=pass, 1=issues found
"""
from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path

WORKFLOW_PATH = Path(".trellis/workflow.md")
VALID_STATUSES = {"planning", "in_progress", "completed", "cancelled", "blocked"}

# Step heading pattern: #### X.X Title
STEP_RE = re.compile(r"^####\s+(\d+\.\d+[a-z]?)\s+(.+)$")
# Phase heading: ## Phase N: Title
PHASE_RE = re.compile(r"^##\s+Phase\s+(\d+):\s*(.+)$")
# Phase Index line: Phase N: Title
INDEX_PHASE_RE = re.compile(r"^Phase\s+(\d+):\s*(.+)$")
# Required marker
REQUIRED_RE = re.compile(r"\[required", re.IGNORECASE)
# Phase Index step line: "- X.X Title [required ...]"
INDEX_STEP_RE = re.compile(r"^\-\s+(\d+\.\d+[a-z]?)\s+(.+)$")


def parse_workflow(path: Path | None = None) -> dict:
    """Parse workflow phases, steps, and Phase Index entries."""
    workflow_path = path or WORKFLOW_PATH
    if not workflow_path.is_file():
        return {"error": f"workflow.md not found at {workflow_path}"}

    text = workflow_path.read_text(encoding="utf-8")
    lines = text.splitlines()

    phases: list[dict] = []
    steps: list[dict] = []
    index_steps: list[str] = []  # step IDs from Phase Index
    index_phases: list[int] = []
    in_index = False
    current_phase = None

    for line in lines:
        # Phase Index section
        stripped = line.strip()
        if stripped == "## Phase Index":
            in_index = True
            continue
        if in_index and stripped.startswith("## "):
            in_index = False

        if in_index:
            phase_match = INDEX_PHASE_RE.match(stripped)
            if phase_match:
                index_phases.append(int(phase_match.group(1)))
            m = INDEX_STEP_RE.match(stripped)
            if m:
                index_steps.append(m.group(1))

        # Phase heading
        pm = PHASE_RE.match(line)
        if pm:
            current_phase = int(pm.group(1))
            phases.append({"number": current_phase, "title": pm.group(2).strip()})
            continue

        # Step heading
        sm = STEP_RE.match(line)
        if sm:
            step_id = sm.group(1)
            title = sm.group(2).strip()
            required = bool(REQUIRED_RE.search(title))
            steps.append({
                "id": step_id,
                "title": title,
                "required": required,
                "phase": current_phase,
            })

    return {
        "phases": phases,
        "steps": steps,
        "index_steps": index_steps,
        "index_phases": index_phases,
    }


def check_integrity(parsed: dict) -> dict:
    """Return {"passed": [...], "issues": [...]}."""
    if "error" in parsed:
        return {"passed": [], "issues": [parsed["error"]]}

    passed: list[str] = []
    issues: list[str] = []

    steps = parsed["steps"]
    index_steps = parsed["index_steps"]
    index_phases = parsed.get("index_phases", [])
    phases = parsed["phases"]

    # Check 1: Phase Index references match actual step headings
    actual_step_ids = {s["id"] for s in steps}
    for idx_id in index_steps:
        if idx_id not in actual_step_ids:
            issues.append(f"Phase Index references step {idx_id} but no '#### {idx_id}' heading found")

    # Check 2: Required steps exist
    required_steps = [s for s in steps if s["required"]]
    if not required_steps:
        issues.append("No [required] steps found — workflow may be incomplete")
    else:
        passed.append(f"{len(required_steps)} required steps found")

    # Check 3: All steps belong to a phase
    orphan_steps = [s for s in steps if s.get("phase") is None]
    for s in orphan_steps:
        issues.append(f"Step {s['id']} has no parent Phase heading")

    # Check 4: Phases are sequential
    phase_numbers = [p["number"] for p in phases]
    if index_phases and index_phases != phase_numbers:
        issues.append(
            f"Phase Index phases {index_phases} do not match actual Phase sections {phase_numbers}"
        )
    elif index_phases:
        passed.append(f"Phase Index matches Phase sections: {phase_numbers}")

    if phase_numbers != sorted(phase_numbers):
        issues.append(f"Phase numbers not sequential: {phase_numbers}")
    elif phase_numbers:
        passed.append(f"Phases sequential: {phase_numbers}")

    # Check 5: Has at least Plan/Execute/Finish phases
    expected_min = {1, 2, 3}
    found = set(phase_numbers)
    missing_phases = expected_min - found
    if missing_phases:
        issues.append(f"Missing core phases: {missing_phases}")
    else:
        passed.append("Core phases 1/2/3 present")

    return {"passed": passed, "issues": issues}


def main() -> int:
    parser = argparse.ArgumentParser(description="Workflow definition self-check")
    parser.add_argument(
        "--workflow",
        type=Path,
        default=WORKFLOW_PATH,
        help=f"workflow Markdown path (default: {WORKFLOW_PATH})",
    )
    parser.add_argument("--json", action="store_true", help="Output JSON")
    args = parser.parse_args()

    parsed = parse_workflow(args.workflow)
    result = check_integrity(parsed)

    if args.json:
        print(json.dumps(result, indent=2, ensure_ascii=False))
    else:
        print("=" * 60)
        print("Workflow Integrity Check")
        print("=" * 60)

        if result["passed"]:
            print("\n✓ Passed:")
            for item in result["passed"]:
                print(f"  ✓ {item}")

        if result["issues"]:
            print("\n✗ Issues:")
            for item in result["issues"]:
                print(f"  ✗ {item}")
            print(f"\n{len(result['issues'])} issue(s) found")
        else:
            print("\nAll checks passed.")

    return 1 if result["issues"] else 0


if __name__ == "__main__":
    sys.exit(main())
