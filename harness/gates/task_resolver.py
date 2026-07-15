#!/usr/bin/env python3
"""task_resolver.py — Shared task directory resolution for workflow scripts.

Provides a single ``resolve_task_dir`` function that accepts:

- **Absolute path**: ``/path/to/.trellis/tasks/07-11-my-task``
- **Relative path**: ``.trellis/tasks/07-11-my-task``
- **Bare task name**: ``my-task`` (exact match, then suffix match on ``MM-DD-<name>``)
- **Task name with date prefix**: ``07-11-my-task``

If the task is not found in ``.trellis/tasks/``, the resolver also checks
``.trellis/tasks/archive/`` (including monthly sub-directories) so that
scripts work on archived tasks without special-casing.

This module is the marketplace analogue of Trellis CLI's
``common/task_utils.resolve_task_dir``.  It is vendored here because the
marketplace workflow bundle is installed to
``.trellis/workflows/ai-native-harness-dev/scripts/`` and must not depend
on ``.trellis/scripts/common/`` at runtime.
"""
from __future__ import annotations

from pathlib import Path


def _detect_tasks_dir(start: Path | None = None) -> Path:
    """Walk up from *start* (default CWD) to find ``.trellis/tasks/``."""
    p = (start or Path.cwd()).resolve()
    for cand in [p, *p.parents]:
        candidate = cand / ".trellis" / "tasks"
        if candidate.is_dir():
            return candidate
    raise FileNotFoundError(
        "Could not locate `.trellis/tasks/`. "
        "Run from inside a Trellis project or pass an absolute task path."
    )


def _collect_suffix_matches(task_name: str, directory: Path) -> list[Path]:
    """Return all directories in *directory* whose name ends with ``-{task_name}``."""
    if not task_name or not directory.is_dir():
        return []
    return sorted(
        (d for d in directory.iterdir() if d.is_dir() and d.name.endswith(f"-{task_name}")),
        key=lambda d: d.name,
    )


def _find_by_name(task_name: str, tasks_dir: Path) -> Path | None:
    """Exact match first, then suffix match (``my-task`` → ``07-11-my-task``).

    If multiple suffix matches exist, raise ``FileNotFoundError`` to avoid
    silent non-deterministic resolution.
    """
    if not task_name or not tasks_dir.is_dir():
        return None

    exact = tasks_dir / task_name
    if exact.is_dir():
        return exact

    matches = _collect_suffix_matches(task_name, tasks_dir)
    if len(matches) == 1:
        return matches[0]
    if len(matches) > 1:
        names = ", ".join(m.name for m in matches)
        raise FileNotFoundError(
            f"Ambiguous task name {task_name!r}: matches {names}. "
            f"Use the full directory name to disambiguate."
        )

    return None


def _find_in_archive(task_name: str, tasks_dir: Path) -> Path | None:
    """Search ``tasks/archive/`` and its monthly sub-directories.

    Raises on ambiguous suffix matches (same logic as ``_find_by_name``).
    """
    archive_root = tasks_dir / "archive"
    if not archive_root.is_dir():
        return None

    # Direct: archive/<task_name>
    direct = archive_root / task_name
    if direct.is_dir():
        return direct

    # Monthly sub-dirs: archive/2026-07/<task_name>
    for month_dir in sorted(archive_root.iterdir(), key=lambda d: d.name):
        if not month_dir.is_dir():
            continue
        candidate = month_dir / task_name
        if candidate.is_dir():
            return candidate
        # Suffix match inside monthly dir
        matches = _collect_suffix_matches(task_name, month_dir)
        if len(matches) == 1:
            return matches[0]
        if len(matches) > 1:
            names = ", ".join(m.name for m in matches)
            raise FileNotFoundError(
                f"Ambiguous task name {task_name!r} in {month_dir.name}: matches {names}. "
                f"Use the full directory name to disambiguate."
            )

    # Suffix match at archive root level
    matches = _collect_suffix_matches(task_name, archive_root)
    if len(matches) == 1:
        return matches[0]
    if len(matches) > 1:
        names = ", ".join(m.name for m in matches)
        raise FileNotFoundError(
            f"Ambiguous task name {task_name!r} in archive: matches {names}. "
            f"Use the full directory name to disambiguate."
        )

    return None


def _verify_containment(resolved: Path, project_root: Path) -> Path:
    """Ensure *resolved* is within *project_root* after symlink resolution.

    Raises ``FileNotFoundError`` if the path escapes the project root.
    """
    real_resolved = resolved.resolve()
    real_root = project_root.resolve()
    try:
        real_resolved.relative_to(real_root)
    except ValueError:
        raise FileNotFoundError(
            f"Resolved task path escapes project root: {resolved} "
            f"(real: {real_resolved}, root: {real_root})"
        )
    return real_resolved


def resolve_task_dir(
    task_input: str,
    *,
    tasks_dir: Path | None = None,
) -> Path:
    """Resolve *task_input* to an absolute task directory path.

    Args:
        task_input: Task name, relative path, or absolute path.
            - Bare name: ``my-task`` (exact match, then suffix match)
            - Date-prefix name: ``07-11-my-task``
            - Relative path: ``.trellis/tasks/07-11-my-task``
            - Absolute path: ``/abs/.trellis/tasks/07-11-my-task``
        tasks_dir: Override for the ``.trellis/tasks/`` directory.
            Defaults to auto-detection from CWD.

    Returns:
        Absolute :class:`~pathlib.Path` to the task directory, verified
        to be within the project root.

    Raises:
        FileNotFoundError: If the task directory cannot be resolved,
            or if the resolved path escapes the project root.
    """
    if not task_input:
        raise FileNotFoundError("Empty task identifier.")

    # Workdir pass-through: if task_input names an existing directory (absolute,
    # or relative to CWD), use it directly. This lets the gates run against a
    # Multica local_directory workdir that has no .trellis/tasks/ layout and no
    # task.json. A bare name (no path separator, no leading dot) still falls
    # through to the Trellis name resolution below, so existing Trellis usage
    # is unchanged.
    norm = task_input.replace("\\", "/")
    looks_like_path = "/" in norm or norm.startswith(".") or Path(task_input).is_absolute()
    if looks_like_path:
        cand = Path(task_input)
        cand = cand.resolve() if cand.is_absolute() else (Path.cwd() / task_input).resolve()
        if cand.is_dir():
            return cand

    if tasks_dir is None:
        tasks_dir = _detect_tasks_dir()

    project_root = tasks_dir.parent.parent  # .trellis/tasks → project root

    # 1. Absolute path — derive project root from the path itself by
    #    walking up to find .trellis/tasks/, then verify containment.
    p = Path(task_input)
    if p.is_absolute():
        if not p.is_dir():
            raise FileNotFoundError(f"Task directory not found: {p}")
        # Walk up from p to find .trellis/tasks/ — that identifies the project root.
        abs_project_root: Path | None = None
        for cand in [p, *p.parents]:
            if (cand / ".trellis" / "tasks").is_dir():
                abs_project_root = cand
                break
        if abs_project_root is None:
            raise FileNotFoundError(
                f"Path is not inside a Trellis project (no .trellis/tasks/ found above): {p}"
            )
        verified = _verify_containment(p, abs_project_root)
        if not (verified / "task.json").is_file():
            raise FileNotFoundError(
                f"Not a valid task directory (no task.json): {p}"
            )
        return verified

    # 2. Relative path — must contain ``/`` and resolve within project root.
    normalized = task_input.replace("\\", "/")
    while normalized.startswith("./"):
        normalized = normalized[2:]
    if "/" in normalized or normalized.startswith(".trellis"):
        resolved = (project_root / normalized).resolve()
        if resolved.is_dir():
            return _verify_containment(resolved, project_root)
        # Fall through to name-based lookup in case the path was a false positive.

    # 3. Bare task name — exact then suffix match, with archive fallback.
    found = _find_by_name(task_input, tasks_dir)
    if found:
        return _verify_containment(found, project_root)

    found = _find_in_archive(task_input, tasks_dir)
    if found:
        return _verify_containment(found, project_root)

    raise FileNotFoundError(
        f"Task directory not found: {task_input!r}. "
        f"Searched in {tasks_dir} and its archive."
    )
