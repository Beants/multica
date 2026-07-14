#!/usr/bin/env python3
"""Validate requirement-to-case traceability and execution evidence."""
from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path
from typing import Iterable


REQUIREMENT_ID = re.compile(r"^REQ-[A-Z0-9]+(?:-[A-Z0-9]+)*$")
CASE_ID = re.compile(r"^(BC|TC)-[A-Z0-9]+(?:-[A-Z0-9]+)*$")
PRIORITIES = {"P0", "P1", "P2"}
APPLICABILITY = {"applicable", "not_applicable"}
RESULT_STATUSES = {"PASS", "FAIL", "BLOCKED", "NOT_APPLICABLE", "SKIPPED"}


class ContractInputError(ValueError):
    """Raised when JSON input cannot be parsed as the contract format."""


def _percentage(numerator: int, denominator: int) -> float:
    return round((numerator / denominator) * 100, 2) if denominator else 100.0


def _non_empty_string(value: object) -> bool:
    return isinstance(value, str) and bool(value.strip())


def parse_results(lines: Iterable[str]) -> list[dict]:
    results: list[dict] = []
    for line_number, raw in enumerate(lines, start=1):
        if not raw.strip():
            continue
        try:
            value = json.loads(raw)
        except json.JSONDecodeError as exc:
            raise ContractInputError(
                f"case results line {line_number} is invalid JSON: {exc.msg}"
            ) from exc
        if not isinstance(value, dict):
            raise ContractInputError(
                f"case results line {line_number} must be a JSON object"
            )
        results.append(value)
    return results


def _latest_results(results: list[dict]) -> dict[str, dict]:
    latest: dict[str, dict] = {}
    for event in results:
        case_id = event.get("case_id")
        timestamp = event.get("ts")
        if not _non_empty_string(case_id) or not _non_empty_string(timestamp):
            continue
        current = latest.get(case_id)
        if current is None or str(timestamp) >= str(current.get("ts", "")):
            latest[str(case_id)] = event
    return latest


def check_contract(
    contract: dict,
    results: list[dict],
    *,
    require_frozen: bool,
    require_results: bool,
) -> dict:
    issues: list[str] = []
    warnings: list[str] = []

    if contract.get("schema") != 1:
        issues.append("contract schema must be 1")
    spec_revision = contract.get("spec_revision")
    if not isinstance(spec_revision, int) or spec_revision < 1:
        issues.append("spec_revision must be a positive integer")

    freeze = contract.get("freeze")
    if not isinstance(freeze, dict):
        freeze = {}
        issues.append("freeze must be an object")
    freeze_status = freeze.get("status")
    if freeze_status not in {"draft", "frozen", "reopened"}:
        issues.append("freeze.status must be draft, frozen, or reopened")
    if require_frozen and freeze_status != "frozen":
        issues.append("contract must be frozen")
    if freeze_status == "frozen":
        for field in ("approved_by", "approved_at"):
            if not _non_empty_string(freeze.get(field)):
                issues.append(f"frozen contract requires freeze.{field}")

    requirements = contract.get("requirements")
    cases = contract.get("cases")
    if not isinstance(requirements, list):
        requirements = []
        issues.append("requirements must be an array")
    if not isinstance(cases, list):
        cases = []
        issues.append("cases must be an array")

    requirement_by_id: dict[str, dict] = {}
    for index, requirement in enumerate(requirements):
        if not isinstance(requirement, dict):
            issues.append(f"requirements[{index}] must be an object")
            continue
        requirement_id = requirement.get("id")
        if not _non_empty_string(requirement_id) or not REQUIREMENT_ID.fullmatch(
            str(requirement_id)
        ):
            issues.append(f"requirements[{index}].id must match REQ-*")
            continue
        if requirement_id in requirement_by_id:
            issues.append(f"duplicate requirement id: {requirement_id}")
        requirement_by_id[str(requirement_id)] = requirement
        if requirement.get("applicability") not in APPLICABILITY:
            issues.append(
                f"requirement {requirement_id} applicability must be applicable or not_applicable"
            )
        elif requirement.get("applicability") == "not_applicable" and not _non_empty_string(
            requirement.get("applicability_reason")
        ):
            issues.append(
                f"requirement {requirement_id} not_applicable requires applicability_reason"
            )

    case_by_id: dict[str, dict] = {}
    business_ids: set[str] = set()
    for index, case in enumerate(cases):
        if not isinstance(case, dict):
            issues.append(f"cases[{index}] must be an object")
            continue
        case_id = case.get("id")
        match = CASE_ID.fullmatch(str(case_id)) if _non_empty_string(case_id) else None
        if match is None:
            issues.append(f"cases[{index}].id must match BC-* or TC-*")
            continue
        case_id = str(case_id)
        if case_id in case_by_id:
            issues.append(f"duplicate case id: {case_id}")
        case_by_id[case_id] = case
        expected_level = "business" if match.group(1) == "BC" else "technical"
        if case.get("level") != expected_level:
            issues.append(f"case {case_id} level must be {expected_level}")
        if expected_level == "business":
            business_ids.add(case_id)
        if case.get("priority") not in PRIORITIES:
            issues.append(f"case {case_id} priority must be P0, P1, or P2")
        if case.get("applicability") not in APPLICABILITY:
            issues.append(
                f"case {case_id} applicability must be applicable or not_applicable"
            )
        elif case.get("applicability") == "not_applicable" and not _non_empty_string(
            case.get("applicability_reason")
        ):
            issues.append(f"case {case_id} not_applicable requires applicability_reason")
        automatable = case.get("automatable")
        if not isinstance(automatable, bool):
            issues.append(f"case {case_id} automatable must be boolean")
        elif automatable and case.get("applicability") == "applicable":
            automation = case.get("automation")
            if not isinstance(automation, dict) or any(
                not _non_empty_string(automation.get(field))
                for field in ("selector", "command", "environment")
            ):
                issues.append(
                    f"case {case_id} automation requires selector, command, and environment"
                )
        elif not automatable and case.get("applicability") == "applicable":
            if not _non_empty_string(case.get("manual_reason")):
                issues.append(f"case {case_id} requires manual_reason")

    mapped_requirements: set[str] = set()
    for case_id, case in case_by_id.items():
        if case.get("level") == "business":
            requirement_ids = case.get("requirement_ids")
            if not isinstance(requirement_ids, list) or not requirement_ids:
                issues.append(f"business case {case_id} requires requirement_ids")
                continue
            for requirement_id in requirement_ids:
                if requirement_id not in requirement_by_id:
                    issues.append(
                        f"business case {case_id} references unknown requirement {requirement_id}"
                    )
                else:
                    mapped_requirements.add(str(requirement_id))
        elif case.get("level") == "technical":
            parent_ids = case.get("derives_from")
            if not isinstance(parent_ids, list) or not parent_ids:
                issues.append(f"technical case {case_id} requires derives_from")
                continue
            for parent_id in parent_ids:
                if parent_id not in business_ids:
                    issues.append(
                        f"technical case {case_id} references unknown business case {parent_id}"
                    )

    applicable_requirements = {
        requirement_id
        for requirement_id, requirement in requirement_by_id.items()
        if requirement.get("applicability") == "applicable"
    }
    for requirement_id in sorted(applicable_requirements - mapped_requirements):
        issues.append(f"applicable requirement {requirement_id} has no business case")

    for index, event in enumerate(results):
        if event.get("schema") != 1:
            issues.append(f"result event {index + 1} schema must be 1")
        result_case_id = event.get("case_id")
        if not _non_empty_string(result_case_id):
            issues.append(f"result event {index + 1} requires case_id")
        elif result_case_id not in case_by_id:
            issues.append(
                f"result event {index + 1} references unknown case {result_case_id}"
            )
        if not _non_empty_string(event.get("ts")):
            issues.append(f"result event {index + 1} requires ts")
        if not _non_empty_string(event.get("command")):
            issues.append(f"result event {index + 1} requires command")
        if not isinstance(event.get("spec_revision"), int):
            issues.append(f"result event {index + 1} requires integer spec_revision")
        if event.get("status") not in RESULT_STATUSES:
            issues.append(
                f"result event {index + 1} has invalid status {event.get('status')!r}"
            )

    latest = _latest_results(results)
    eligible_cases = {
        case_id: case
        for case_id, case in case_by_id.items()
        if case.get("applicability") == "applicable"
        and case.get("priority") in {"P0", "P1"}
    }
    current_events: dict[str, dict] = {}
    for case_id, case in eligible_cases.items():
        event = latest.get(case_id)
        if event is None:
            if require_results:
                issues.append(f"case {case_id} has no execution result")
            continue
        if event.get("spec_revision") != spec_revision:
            if require_results:
                issues.append(
                    f"case {case_id} result is not for current spec revision {spec_revision}"
                )
            continue
        current_events[case_id] = event
        status = event.get("status")
        if status not in RESULT_STATUSES:
            continue
        if require_results and status != "PASS":
            issues.append(f"case {case_id} latest result is {status}, not PASS")
        if status in {"PASS", "FAIL"}:
            evidence = event.get("evidence")
            if (
                not isinstance(evidence, list)
                or not evidence
                or any(not _non_empty_string(item) for item in evidence)
            ):
                issues.append(f"case {case_id} result requires evidence")
        elif status in {"BLOCKED", "NOT_APPLICABLE", "SKIPPED"} and not _non_empty_string(
            event.get("reason")
        ):
            issues.append(f"case {case_id} result status {status} requires reason")

    automatable_cases = [
        case
        for case in case_by_id.values()
        if case.get("applicability") == "applicable" and case.get("automatable") is True
    ]
    mapped_automation = [case for case in automatable_cases if isinstance(case.get("automation"), dict)]
    passed_events = [event for event in current_events.values() if event.get("status") == "PASS"]
    evidenced_events = [
        event
        for event in current_events.values()
        if isinstance(event.get("evidence"), list) and bool(event.get("evidence"))
    ]
    metrics = {
        "requirement_design_coverage": _percentage(
            len(applicable_requirements & mapped_requirements), len(applicable_requirements)
        ),
        "automation_coverage": _percentage(
            len(mapped_automation), len(automatable_cases)
        ),
        "execution_coverage": _percentage(len(current_events), len(eligible_cases)),
        "pass_rate": _percentage(len(passed_events), len(current_events)),
        "evidence_completeness": _percentage(
            len(evidenced_events), len(current_events)
        ),
    }
    if not require_results and not results:
        warnings.append("case results were not evaluated")

    return {
        "schema": 1,
        "spec_revision": spec_revision,
        "issues": issues,
        "warnings": warnings,
        "metrics": metrics,
    }


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("contract", type=Path, help="verification-contract.json path")
    parser.add_argument("--results", type=Path, help="case-results.jsonl path")
    parser.add_argument("--require-frozen", action="store_true")
    parser.add_argument("--require-results", action="store_true")
    args = parser.parse_args(argv)

    try:
        contract = json.loads(args.contract.read_text(encoding="utf-8"))
        if not isinstance(contract, dict):
            raise ContractInputError("contract root must be a JSON object")
        results_path = args.results
        if results_path is None:
            results_path = args.contract.with_name("case-results.jsonl")
        if results_path.exists():
            results = parse_results(results_path.read_text(encoding="utf-8").splitlines())
        else:
            results = []
    except (OSError, json.JSONDecodeError, ContractInputError) as exc:
        print(json.dumps({"error": str(exc)}, ensure_ascii=False))
        return 2

    result = check_contract(
        contract,
        results,
        require_frozen=args.require_frozen,
        require_results=args.require_results,
    )
    print(json.dumps(result, ensure_ascii=False, indent=2))
    return 1 if result["issues"] else 0


if __name__ == "__main__":
    sys.exit(main())
