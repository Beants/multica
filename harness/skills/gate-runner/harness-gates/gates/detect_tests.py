#!/usr/bin/env python3
"""detect_tests.py — 扫描项目测试栈，生成/校验 test-plan.json。

启发式（best-effort，非精确）：扫 Makefile / package.json / AGENTS.md / go.mod /
pyproject.toml / Cargo.toml / playwright.config，按常见命名猜各测试类型命令。
**生成的是草稿**——务必人工核对 cmd 真能跑对应测试（例如 multica 的 `make test` 只
跑 Go，TS 要 `pnpm test`，需人合并成 `make test && pnpm test`）。

用法:
    python3 harness/gates/detect_tests.py                  # 扫 cwd，写 test-plan.json
    python3 harness/gates/detect_tests.py --dir /path      # 扫指定项目根
    python3 harness/gates/detect_tests.py --stdout         # 只打印不写文件
    python3 harness/gates/detect_tests.py check            # 校验现有 test-plan 命令是否还存在
"""
from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path

from task_resolver import harness_root as _harness_root, test_plan_path as _test_plan_path

MAKEFILES = ("Makefile", "makefile", "GNUmakefile")
TARGET_RE = re.compile(r"^([a-zA-Z][\w.-]*):")


def _read(p: Path) -> str:
    try:
        return p.read_text(encoding="utf-8", errors="replace")
    except OSError:
        return ""


def makefile_targets(d: Path) -> set[str]:
    for n in MAKEFILES:
        p = d / n
        if p.is_file():
            return {m.group(1) for line in _read(p).splitlines() if (m := TARGET_RE.match(line))}
    return set()


def package_scripts(d: Path) -> dict[str, str]:
    p = d / "package.json"
    if not p.is_file():
        return {}
    try:
        return json.loads(p.read_text(encoding="utf-8")).get("scripts", {}) or {}
    except (OSError, json.JSONDecodeError):
        return {}


def has(d: Path, *names: str) -> bool:
    return any((d / n).exists() for n in names)


def uses_pytest(d: Path) -> bool:
    for n in ("pyproject.toml", "setup.cfg", "requirements.txt", "requirements-dev.txt"):
        p = d / n
        if p.is_file() and "pytest" in _read(p).lower():
            return True
    return False


def detect(d: Path) -> dict:
    targets = makefile_targets(d)
    scripts = package_scripts(d)
    plan: dict[str, dict] = {}

    def add(key: str, cmds: list[str], hard: bool = True, **extra) -> None:
        cmds = [c for c in cmds if c]
        if not cmds:
            return
        entry = {"cmd": " && ".join(cmds), "hard": hard}
        entry.update(extra)
        plan[key] = entry

    # Each type collects ALL matching commands and merges with && — so a polyglot
    # repo (e.g. multica: `make test`=Go + `pnpm test`=TS) gets both automatically,
    # no human merge step. Language-native runners (go test/pytest/cargo) are only
    # used as a fallback when no Makefile/package.json test target exists (to avoid
    # duplicating what `make test` already runs).
    unit_cmds: list[str] = []
    if "test" in targets:
        unit_cmds.append("make test")
    if "test" in scripts:
        unit_cmds.append("pnpm test")
    if not unit_cmds:
        if has(d, "go.mod"):
            unit_cmds.append("go test ./...")
        elif uses_pytest(d):
            unit_cmds.append("pytest")
        elif has(d, "Cargo.toml"):
            unit_cmds.append("cargo test")
    add("unit", unit_cmds)

    # lint
    lint_cmds: list[str] = []
    if "lint" in targets:
        lint_cmds.append("make lint")
    if "lint" in scripts:
        lint_cmds.append("pnpm lint")
    add("lint", lint_cmds)

    # typecheck
    tc_cmds: list[str] = []
    if "typecheck" in scripts:
        tc_cmds.append("pnpm typecheck")
    elif "tsc" in scripts:
        tc_cmds.append("pnpm tsc")
    if "typecheck" in targets and not tc_cmds:
        tc_cmds.append("make typecheck")
    add("typecheck", tc_cmds)

    # integration
    int_cmds: list[str] = []
    if "integration" in targets or "integ-test" in targets:
        int_cmds.append("make integration")
    if "test:integration" in scripts:
        int_cmds.append("pnpm test:integration")
    add("integration", int_cmds, hard=False)

    # e2e
    e2e_cmds: list[str] = []
    if "e2e" in targets:
        e2e_cmds.append("make e2e")
    elif "test:e2e" in scripts:
        e2e_cmds.append("pnpm test:e2e")
    elif has(d, "playwright.config.ts", "playwright.config.js", "playwright.config.mjs"):
        e2e_cmds.append("npx playwright test")
    add("e2e", e2e_cmds, hard=False, manual=True)

    # api (rarely a dedicated target — absent if none found)
    api_cmds: list[str] = []
    if "api-test" in targets:
        api_cmds.append("make api-test")
    if "test:api" in scripts:
        api_cmds.append("pnpm test:api")
    add("api", api_cmds)

    return plan


def _cmd_resolves(cmd: str, targets: set[str], scripts: dict, d: Path) -> bool:
    parts = cmd.split()
    if not parts:
        return True
    runner = parts[0]
    first = parts[1] if len(parts) > 1 else ""
    if runner == "make" and first:
        return first in targets
    if runner in ("pnpm", "npm", "yarn") and first:
        if first in scripts:
            return True
        if first in ("run", "exec") and len(parts) > 2:
            return parts[2] in scripts
        return False
    if runner == "go" and first == "test":
        return has(d, "go.mod")
    if runner == "pytest":
        return uses_pytest(d)
    if runner == "cargo" and first == "test":
        return has(d, "Cargo.toml")
    if runner == "npx" and "playwright" in cmd:
        return has(d, "playwright.config.ts", "playwright.config.js", "playwright.config.mjs")
    return True  # unknown runner — don't false-flag


def check(d: Path) -> int:
    plan_path = _test_plan_path(d)
    if not plan_path.is_file():
        plan_path = d / "test-plan.json"  # fallback: legacy location
    if not plan_path.is_file():
        print(f"no test-plan.json at {_test_plan_path(d)}; run `detect_tests.py` first", file=sys.stderr)
        return 2
    try:
        plan = json.loads(plan_path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as e:
        print(f"error: {e}", file=sys.stderr)
        return 2
    targets = makefile_targets(d)
    scripts = package_scripts(d)
    stale: list[str] = []
    for key, spec in plan.items():
        cmd = spec.get("cmd") if isinstance(spec, dict) else None
        if not cmd:
            continue
        ok = _cmd_resolves(cmd, targets, scripts, d)
        print(f"  {'ok  ' if ok else 'STALE'} {key}: {cmd}")
        if not ok:
            stale.append(f"{key}: {cmd}")
    if stale:
        print(f"\n{len(stale)} command(s) no longer resolve (target/script removed or renamed):")
        for s in stale:
            print(f"  - {s}")
        return 1
    print("\nall commands resolve.")
    return 0


def main() -> int:
    ap = argparse.ArgumentParser(description="Detect/verify project test stack -> test-plan.json.")
    ap.add_argument("--dir", type=Path, default=Path.cwd(), help="Project root (default: cwd)")
    ap.add_argument("--stdout", action="store_true", help="Print instead of writing test-plan.json")
    ap.add_argument("subcmd", nargs="?", choices=("check",), help="'check' verifies an existing test-plan.json")
    args = ap.parse_args()

    if args.subcmd == "check":
        return check(args.dir)

    plan = detect(args.dir)
    out = json.dumps(plan, indent=2, ensure_ascii=False) + "\n"
    if not plan:
        print("no test stack detected (no Makefile/package.json/go.mod/pyproject/Cargo.toml).", file=sys.stderr)
        return 1
    if args.stdout:
        print(out)
        return 0
    _harness_root(args.dir).mkdir(parents=True, exist_ok=True)
    target = _test_plan_path(args.dir)
    target.write_text(out, encoding="utf-8")
    print(f"wrote {target} ({len(plan)} entries) — multi-stack commands auto-merged with &&.")
    print("run `detect_tests.py check` anytime to verify all commands still resolve.\n")
    print(out)
    return 0


if __name__ == "__main__":
    sys.exit(main())
