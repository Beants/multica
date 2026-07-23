#!/usr/bin/env python3
"""
测试 task_resolver.py 的 parent-issue-id 隔离功能

测试点：
1. 环境变量读取 parent issue id
2. marker 文件读取 parent issue id
3. 无 parent id 时的 fallback 逻辑
4. 路径生成正确性
5. 目录创建和隔离
"""

import os
import sys
import tempfile
import unittest
from pathlib import Path

# 添加 task_resolver.py 所在目录到 Python 路径
sys.path.insert(0, str(Path(__file__).parent / "harness" / "skills" / "gate-runner" / "harness-gates" / "gates"))

from task_resolver import (
    _read_parent_issue_id,
    harness_root,
    specs_dir,
    review_dir,
    evidence_dir,
    baseline_dir,
    test_plan_path,
    ensure_harness_dirs,
    _DEFAULT_PARENT_KEY,
    HARNESS_DIRNAME,
)


class TestParentIssueIdIsolation(unittest.TestCase):
    """测试 parent-issue-id 隔离功能"""

    def setUp(self):
        """创建临时目录"""
        self.temp_dir = tempfile.mkdtemp()
        self.task_dir = Path(self.temp_dir) / "task"
        self.task_dir.mkdir()

        # 清理环境变量
        if "HARNESS_PARENT_ISSUE_ID" in os.environ:
            del os.environ["HARNESS_PARENT_ISSUE_ID"]

    def tearDown(self):
        """清理临时目录和环境变量"""
        import shutil
        shutil.rmtree(self.temp_dir, ignore_errors=True)

        if "HARNESS_PARENT_ISSUE_ID" in os.environ:
            del os.environ["HARNESS_PARENT_ISSUE_ID"]

    def test_read_parent_issue_id_from_env(self):
        """测试从环境变量读取 parent issue id"""
        test_pid = "test-parent-123"
        os.environ["HARNESS_PARENT_ISSUE_ID"] = test_pid

        result = _read_parent_issue_id(self.task_dir)

        self.assertEqual(result, test_pid)

    def test_read_parent_issue_id_from_marker(self):
        """测试从 marker 文件读取 parent issue id"""
        test_pid = "marker-parent-456"

        # 创建 .harness/.parent 文件
        harness_dir = self.task_dir / HARNESS_DIRNAME
        harness_dir.mkdir()
        marker_file = harness_dir / ".parent"
        marker_file.write_text(test_pid)

        result = _read_parent_issue_id(self.task_dir)

        self.assertEqual(result, test_pid)

    def test_read_parent_issue_id_env_priority(self):
        """测试环境变量优先级高于 marker 文件"""
        env_pid = "env-parent-789"
        marker_pid = "marker-parent-012"

        os.environ["HARNESS_PARENT_ISSUE_ID"] = env_pid

        # 创建 marker 文件
        harness_dir = self.task_dir / HARNESS_DIRNAME
        harness_dir.mkdir()
        marker_file = harness_dir / ".parent"
        marker_file.write_text(marker_pid)

        result = _read_parent_issue_id(self.task_dir)

        # 环境变量应该优先
        self.assertEqual(result, env_pid)

    def test_read_parent_issue_id_fallback(self):
        """测试无 parent id 时返回 None"""
        result = _read_parent_issue_id(self.task_dir)

        self.assertIsNone(result)

    def test_harness_root_with_explicit_pid(self):
        """测试显式指定 parent_issue_id 时的路径生成"""
        test_pid = "explicit-parent-345"

        result = harness_root(self.task_dir, test_pid)

        expected = self.task_dir / HARNESS_DIRNAME / test_pid
        self.assertEqual(result, expected)

    def test_harness_root_with_env_pid(self):
        """测试从环境变量读取 parent_issue_id 时的路径生成"""
        test_pid = "env-parent-678"
        os.environ["HARNESS_PARENT_ISSUE_ID"] = test_pid

        result = harness_root(self.task_dir)

        expected = self.task_dir / HARNESS_DIRNAME / test_pid
        self.assertEqual(result, expected)

    def test_harness_root_fallback_to_default(self):
        """测试无 parent id 时 fallback 到 _default"""
        result = harness_root(self.task_dir)

        expected = self.task_dir / HARNESS_DIRNAME / _DEFAULT_PARENT_KEY
        self.assertEqual(result, expected)

    def test_specs_dir_with_pid(self):
        """测试 specs_dir 路径生成"""
        test_pid = "specs-parent-901"

        result = specs_dir(self.task_dir, test_pid)

        expected = self.task_dir / HARNESS_DIRNAME / test_pid / "specs"
        self.assertEqual(result, expected)

    def test_review_dir_with_pid(self):
        """测试 review_dir 路径生成"""
        test_pid = "review-parent-234"

        result = review_dir(self.task_dir, test_pid)

        expected = self.task_dir / HARNESS_DIRNAME / test_pid / "review"
        self.assertEqual(result, expected)

    def test_evidence_dir_with_pid(self):
        """测试 evidence_dir 路径生成"""
        test_pid = "evidence-parent-567"

        result = evidence_dir(self.task_dir, test_pid)

        expected = self.task_dir / HARNESS_DIRNAME / test_pid / "evidence"
        self.assertEqual(result, expected)

    def test_baseline_dir_with_pid(self):
        """测试 baseline_dir 路径生成"""
        test_pid = "baseline-parent-890"

        result = baseline_dir(self.task_dir, test_pid)

        expected = self.task_dir / HARNESS_DIRNAME / test_pid / "evidence" / "baseline"
        self.assertEqual(result, expected)

    def test_test_plan_path_with_pid(self):
        """测试 test_plan_path 路径生成"""
        test_pid = "plan-parent-123"

        result = test_plan_path(self.task_dir, test_pid)

        expected = self.task_dir / HARNESS_DIRNAME / test_pid / "test-plan.json"
        self.assertEqual(result, expected)

    def test_ensure_harness_dirs_creates_structure(self):
        """测试 ensure_harness_dirs 创建目录结构"""
        test_pid = "ensure-parent-456"

        ensure_harness_dirs(self.task_dir, test_pid)

        # 验证目录已创建
        self.assertTrue((self.task_dir / HARNESS_DIRNAME / test_pid / "specs").exists())
        self.assertTrue((self.task_dir / HARNESS_DIRNAME / test_pid / "review").exists())
        self.assertTrue((self.task_dir / HARNESS_DIRNAME / test_pid / "evidence" / "baseline").exists())

    def test_isolation_between_different_pids(self):
        """测试不同 parent id 之间的隔离性"""
        pid1 = "parent-A-111"
        pid2 = "parent-B-222"

        # 创建两个不同的目录结构
        ensure_harness_dirs(self.task_dir, pid1)
        ensure_harness_dirs(self.task_dir, pid2)

        # 验证路径不同
        specs1 = specs_dir(self.task_dir, pid1)
        specs2 = specs_dir(self.task_dir, pid2)

        self.assertNotEqual(specs1, specs2)
        self.assertIn(pid1, str(specs1))
        self.assertIn(pid2, str(specs2))

        # 验证文件隔离
        test_file1 = specs1 / "test.txt"
        test_file1.write_text("content from parent A")

        test_file2 = specs2 / "test.txt"
        self.assertFalse(test_file2.exists())

    def test_backward_compatibility_without_pid(self):
        """测试向后兼容性（无 parent id 时使用 _default）"""
        # 不设置环境变量，不创建 marker 文件
        result = harness_root(self.task_dir)

        expected = self.task_dir / HARNESS_DIRNAME / _DEFAULT_PARENT_KEY
        self.assertEqual(result, expected)

        # 创建目录结构
        ensure_harness_dirs(self.task_dir)

        # 验证 _default 目录已创建
        self.assertTrue((self.task_dir / HARNESS_DIRNAME / _DEFAULT_PARENT_KEY / "specs").exists())


class TestPathConsistency(unittest.TestCase):
    """测试路径一致性"""

    def setUp(self):
        """创建临时目录"""
        self.temp_dir = tempfile.mkdtemp()
        self.task_dir = Path(self.temp_dir) / "task"
        self.task_dir.mkdir()

    def tearDown(self):
        """清理临时目录"""
        import shutil
        shutil.rmtree(self.temp_dir, ignore_errors=True)

    def test_all_paths_use_same_root(self):
        """测试所有路径函数使用相同的 root"""
        test_pid = "consistency-parent-789"

        root = harness_root(self.task_dir, test_pid)
        specs = specs_dir(self.task_dir, test_pid)
        review = review_dir(self.task_dir, test_pid)
        evidence = evidence_dir(self.task_dir, test_pid)
        baseline = baseline_dir(self.task_dir, test_pid)
        plan = test_plan_path(self.task_dir, test_pid)

        # 所有路径都应该以 root 为前缀
        self.assertTrue(str(specs).startswith(str(root)))
        self.assertTrue(str(review).startswith(str(root)))
        self.assertTrue(str(evidence).startswith(str(root)))
        self.assertTrue(str(baseline).startswith(str(root)))
        self.assertTrue(str(plan).startswith(str(root)))


if __name__ == "__main__":
    unittest.main(verbosity=2)
