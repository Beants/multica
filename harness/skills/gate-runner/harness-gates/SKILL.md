---
name: harness-gates
description: Harness 门禁脚本集。跑门禁时用这里面的脚本，不要自己写。脚本路径在 SKILL.md 下方的「调用方式」里。
---

# Harness Gates

门禁脚本位于本 skill 目录下的 `gates/` 子目录。agent 运行时，daemon 把本 skill 写到 workdir 的 provider 原生 skill 目录（如 `.claude/skills/harness-gates/`），脚本就在 `.claude/skills/harness-gates/gates/` 下。

## 脚本清单

| 脚本 | 用途 |
|---|---|
| `plan_contract_check.py` | 检查 prd/design 存在 + 段落完整性 |
| `baseline.py` | 测试快照 before/after + diff（B−A 新增失败检测） |
| `api_gate.py` | 接口测试门禁（只跑 test-plan 的 api 键） |
| `gate_result.py` | 门禁事件追加到 gate-result.jsonl |
| `rollback_counter.py` | 回退计数 + 熔断 |
| `detect_tests.py` | 扫描项目测试栈，生成 test-plan.json 草稿 |
| `delivery_checklist.py` | 交付完整性检查 |
| `scar_summary.py` | 软门禁伤疤汇总 |
| `spec_freshness.py` | spec 新鲜度检查 |
| `gate_prd_confirm.py` | PRD 确认门禁 |
| `task_resolver.py` | 任务路径解析（被其他脚本 import） |
| `verification_contract_check.py` | 验证契约检查 |
| `workflow_integrity_check.py` | 流水线完整性检查 |

## 调用方式

脚本路径前缀取决于你的 provider skill 目录。通用写法：

```bash
# 自动定位 gates 目录（兼容所有 provider）
GATES_DIR="$(find . -path '*/harness-gates/gates' -type d 2>/dev/null | head -1)"
if [ -z "$GATES_DIR" ]; then
  echo "ERROR: harness-gates skill 目录未找到" >&2
  exit 1
fi

# 阶段 2 确定门禁基线
python3 "$GATES_DIR/plan_contract_check.py" --task .
python3 "$GATES_DIR/baseline.py" snapshot --task . --phase before --exclude api

# 阶段 4 基础测试门禁
python3 "$GATES_DIR/baseline.py" snapshot --task . --phase after --exclude api
python3 "$GATES_DIR/baseline.py" diff --task .

# 阶段 5 接口测试门禁（无 api 键 → exit 0 SKIP）
python3 "$GATES_DIR/api_gate.py" snapshot --task . --phase after
python3 "$GATES_DIR/api_gate.py" diff --task .

# 每次跑门禁都追加事件
python3 "$GATES_DIR/gate_result.py" append \
  --task . --phase <plan|implement|check|finish> \
  --gate <name> --status <pass|fail|warn|skipped> \
  --command "<cmd>" --duration-ms <ms> --summary "<summary>" [--hard|--soft]

# 回退计数（队长调用）
python3 "$GATES_DIR/rollback_counter.py" --task <workdir> --record --phase "<M→N>"
```

## 注意

- `--task .` 表示当前 workdir。也可传绝对路径。
- 脚本之间有 sibling import（如 `api_gate.py` import `baseline`），靠 `sys.path[0]` 解析，只要脚本在同一个目录就行。
- 脚本是 harness 预置工程资产，**只跑不改**。
- `task_resolver.py` 不直接跑，被其他脚本 import。
