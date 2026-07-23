#!/usr/bin/env python3
"""Test task_resolver.py parent-issue-id isolation feature.

Tests the new parent_issue_id parameter added to harness directory functions
to ensure different requirements don't interfere with each other.
"""
import os
import tempfile
from pathlib import Path
import sys

# Add the gates directory to path
sys.path.insert(0, str(Path(__file__).parent / "harness-gates" / "gates"))

from task_resolver import (
    _read_parent_issue_id,
    harness_root,
    specs_dir,
    review_dir,
    evidence_dir,
    baseline_dir,
    test_plan_path,
    ensure_harness_dirs,
    HARNESS_DIRNAME,
    _DEFAULT_PARENT_KEY,
)


def test_read_parent_issue_id_from_env():
    """Test reading parent issue ID from environment variable."""
    with tempfile.TemporaryDirectory() as tmpdir:
        task_dir = Path(tmpdir)

        # Set environment variable
        os.environ["HARNESS_PARENT_ISSUE_ID"] = "test-parent-123"
        try:
            result = _read_parent_issue_id(task_dir)
            assert result == "test-parent-123", f"Expected 'test-parent-123', got {result}"
            print("✓ test_read_parent_issue_id_from_env passed")
        finally:
            del os.environ["HARNESS_PARENT_ISSUE_ID"]


def test_read_parent_issue_id_from_marker():
    """Test reading parent issue ID from .parent marker file."""
    with tempfile.TemporaryDirectory() as tmpdir:
        task_dir = Path(tmpdir)

        # Create .harness/.parent marker file
        harness_dir = task_dir / HARNESS_DIRNAME
        harness_dir.mkdir()
        marker_file = harness_dir / ".parent"
        marker_file.write_text("marker-parent-456\n")

        # Ensure env var is not set
        if "HARNESS_PARENT_ISSUE_ID" in os.environ:
            del os.environ["HARNESS_PARENT_ISSUE_ID"]

        result = _read_parent_issue_id(task_dir)
        assert result == "marker-parent-456", f"Expected 'marker-parent-456', got {result}"
        print("✓ test_read_parent_issue_id_from_marker passed")


def test_read_parent_issue_id_none():
    """Test reading parent issue ID when neither env nor marker exists."""
    with tempfile.TemporaryDirectory() as tmpdir:
        task_dir = Path(tmpdir)

        # Ensure env var is not set
        if "HARNESS_PARENT_ISSUE_ID" in os.environ:
            del os.environ["HARNESS_PARENT_ISSUE_ID"]

        result = _read_parent_issue_id(task_dir)
        assert result is None, f"Expected None, got {result}"
        print("✓ test_read_parent_issue_id_none passed")


def test_harness_root_with_parent_id():
    """Test harness_root with explicit parent_issue_id."""
    with tempfile.TemporaryDirectory() as tmpdir:
        task_dir = Path(tmpdir)

        result = harness_root(task_dir, "explicit-parent-789")
        expected = task_dir / HARNESS_DIRNAME / "explicit-parent-789"
        assert result == expected, f"Expected {expected}, got {result}"
        print("✓ test_harness_root_with_parent_id passed")


def test_harness_root_with_env_parent_id():
    """Test harness_root reading parent_issue_id from environment."""
    with tempfile.TemporaryDirectory() as tmpdir:
        task_dir = Path(tmpdir)

        os.environ["HARNESS_PARENT_ISSUE_ID"] = "env-parent-999"
        try:
            result = harness_root(task_dir)
            expected = task_dir / HARNESS_DIRNAME / "env-parent-999"
            assert result == expected, f"Expected {expected}, got {result}"
            print("✓ test_harness_root_with_env_parent_id passed")
        finally:
            del os.environ["HARNESS_PARENT_ISSUE_ID"]


def test_harness_root_fallback():
    """Test harness_root fallback to _default when no parent_issue_id."""
    with tempfile.TemporaryDirectory() as tmpdir:
        task_dir = Path(tmpdir)

        # Ensure env var is not set
        if "HARNESS_PARENT_ISSUE_ID" in os.environ:
            del os.environ["HARNESS_PARENT_ISSUE_ID"]

        result = harness_root(task_dir)
        expected = task_dir / HARNESS_DIRNAME / _DEFAULT_PARENT_KEY
        assert result == expected, f"Expected {expected}, got {result}"
        print("✓ test_harness_root_fallback passed")


def test_specs_dir_with_parent_id():
    """Test specs_dir with parent_issue_id."""
    with tempfile.TemporaryDirectory() as tmpdir:
        task_dir = Path(tmpdir)

        result = specs_dir(task_dir, "specs-parent-111")
        expected = task_dir / HARNESS_DIRNAME / "specs-parent-111" / "specs"
        assert result == expected, f"Expected {expected}, got {result}"
        print("✓ test_specs_dir_with_parent_id passed")


def test_review_dir_with_parent_id():
    """Test review_dir with parent_issue_id."""
    with tempfile.TemporaryDirectory() as tmpdir:
        task_dir = Path(tmpdir)

        result = review_dir(task_dir, "review-parent-222")
        expected = task_dir / HARNESS_DIRNAME / "review-parent-222" / "review"
        assert result == expected, f"Expected {expected}, got {result}"
        print("✓ test_review_dir_with_parent_id passed")


def test_evidence_dir_with_parent_id():
    """Test evidence_dir with parent_issue_id."""
    with tempfile.TemporaryDirectory() as tmpdir:
        task_dir = Path(tmpdir)

        result = evidence_dir(task_dir, "evidence-parent-333")
        expected = task_dir / HARNESS_DIRNAME / "evidence-parent-333" / "evidence"
        assert result == expected, f"Expected {expected}, got {result}"
        print("✓ test_evidence_dir_with_parent_id passed")


def test_baseline_dir_with_parent_id():
    """Test baseline_dir with parent_issue_id."""
    with tempfile.TemporaryDirectory() as tmpdir:
        task_dir = Path(tmpdir)

        result = baseline_dir(task_dir, "baseline-parent-444")
        expected = task_dir / HARNESS_DIRNAME / "baseline-parent-444" / "evidence" / "baseline"
        assert result == expected, f"Expected {expected}, got {result}"
        print("✓ test_baseline_dir_with_parent_id passed")


def test_test_plan_path_with_parent_id():
    """Test test_plan_path with parent_issue_id."""
    with tempfile.TemporaryDirectory() as tmpdir:
        task_dir = Path(tmpdir)

        result = test_plan_path(task_dir, "plan-parent-555")
        expected = task_dir / HARNESS_DIRNAME / "plan-parent-555" / "test-plan.json"
        assert result == expected, f"Expected {expected}, got {result}"
        print("✓ test_test_plan_path_with_parent_id passed")


def test_ensure_harness_dirs():
    """Test ensure_harness_dirs creates correct directory structure."""
    with tempfile.TemporaryDirectory() as tmpdir:
        task_dir = Path(tmpdir)

        ensure_harness_dirs(task_dir, "ensure-parent-666")

        # Check that directories were created
        assert (task_dir / HARNESS_DIRNAME / "ensure-parent-666" / "specs").is_dir()
        assert (task_dir / HARNESS_DIRNAME / "ensure-parent-666" / "review").is_dir()
        assert (task_dir / HARNESS_DIRNAME / "ensure-parent-666" / "evidence" / "baseline").is_dir()
        print("✓ test_ensure_harness_dirs passed")


def test_isolation_between_parents():
    """Test that different parent_issue_ids create isolated directories."""
    with tempfile.TemporaryDirectory() as tmpdir:
        task_dir = Path(tmpdir)

        # Create directories for two different parents
        ensure_harness_dirs(task_dir, "parent-A")
        ensure_harness_dirs(task_dir, "parent-B")

        # Verify isolation
        specs_a = specs_dir(task_dir, "parent-A")
        specs_b = specs_dir(task_dir, "parent-B")

        assert specs_a != specs_b, "Different parents should have different specs dirs"
        assert specs_a.parent.name == "parent-A"
        assert specs_b.parent.name == "parent-B"

        # Create a file in parent-A's specs
        test_file_a = specs_a / "test.txt"
        test_file_a.write_text("content from A")

        # Verify parent-B doesn't see it
        test_file_b = specs_b / "test.txt"
        assert not test_file_b.exists(), "parent-B should not see parent-A's files"

        print("✓ test_isolation_between_parents passed")


if __name__ == "__main__":
    print("Running task_resolver.py parent-issue-id tests...\n")

    test_read_parent_issue_id_from_env()
    test_read_parent_issue_id_from_marker()
    test_read_parent_issue_id_none()
    test_harness_root_with_parent_id()
    test_harness_root_with_env_parent_id()
    test_harness_root_fallback()
    test_specs_dir_with_parent_id()
    test_review_dir_with_parent_id()
    test_evidence_dir_with_parent_id()
    test_baseline_dir_with_parent_id()
    test_test_plan_path_with_parent_id()
    test_ensure_harness_dirs()
    test_isolation_between_parents()

    print("\n✅ All tests passed!")
