# P0 Workflow Engine — Delivery Docs

The P0 workflow engine turns a requirement (intake issue) into an ordered
chain of agent/human steps — plan → gates → implement → gates → review →
acceptance → end — driven by a self-hosted DB state machine inside this
repository. Templates are published with frozen snapshots; runs advance on
verdicts (system-derived for executor steps, agent-written for evaluator
steps); acceptances park the run for a human decision; rejections trigger
targeted rework with downstream invalidation; dual circuit breakers pause
runaway loops. Everything is gated behind the `workflow_engine` feature
flag (default OFF).

## Authoritative documents

The authoritative, living versions of these documents are the Trellis task
artifacts in the **workspace root** of this fork's development monorepo
(`multica-ide/.trellis/tasks/`), not in this repository:

- Task (P0 execution): `.trellis/tasks/07-18-p0-standard-requirement-e2e/`
  — `prd.md` (R1–R10 + AC1–AC9), `design.md` (execution design), `implement.md`
  (wave plan + gates)
- Blueprint: `.trellis/tasks/archive/2026-07/07-17-agent-ide-blueprint/` —
  entity schema, concurrency/idempotency rules, fork hygiene
- Mechanism inventory: `.trellis/tasks/archive/2026-07/07-17-harness-mechanism-inventory/inventory.md`
  — harness mechanism differences D-1~D-12

## This directory (delivery snapshots)

These are **dated copies** of the documents above, shipped with the
`feat/workflow-p0` branch so the design intent travels with the code
("All In Code"). Snapshot date: **2026-07-19**. When the copies and the
Trellis originals disagree, the Trellis originals win.

| File | Source (Trellis) |
|---|---|
| `prd.md` | `07-18-p0-standard-requirement-e2e/prd.md` |
| `design.md` | `07-18-p0-standard-requirement-e2e/design.md` |
| `implement.md` | `07-18-p0-standard-requirement-e2e/implement.md` |
| `inventory.md` | `07-17-harness-mechanism-inventory/inventory.md` |
| `blueprint/prd.md` | `07-17-agent-ide-blueprint/prd.md` |
| `blueprint/design.md` | `07-17-agent-ide-blueprint/design.md` |
| `blueprint/roadmap.md` | `07-17-agent-ide-blueprint/roadmap.md` |
| `blueprint/tech-selection.md` | `07-17-agent-ide-blueprint/tech-selection.md` |
| `blueprint/practice-adoption.md` | `07-17-agent-ide-blueprint/practice-adoption.md` |

## Hard constraints the code lives under

- **Migration range**: all fork migrations use the **900+ number range**
  (P0 ships `901_workflow_core.up/down.sql`). Upstream-owned ranges must
  never be touched — this keeps upstream merges conflict-free.
- **Fork touch budget**: changes to upstream files stay within
  **≤3 files × ≤3 lines**. P0 spends exactly three touches:
  `server/cmd/server/router.go` +1 line, `server/cmd/server/main.go` +2
  lines, `packages/core/types/events.ts` +2 lines (union members only).
  Everything else lives in fork-owned files.
- **Feature flag**: every new surface (routes, listeners, CLI targets, UI
  entry, WS events) is gated by `workflow_engine`, default OFF. Flag-off
  behavior is byte-identical to pre-fork: routes 404, listeners no-op, zero
  new event emission.
- **Node types in P0**: `agent` (executor/evaluator role), `acceptance`,
  `end`. Gate nodes and edge-condition evaluation land in P1
  (`edge.condition` stays NULL in P0).
