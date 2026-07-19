"use client";

// step-timeline.tsx — the step_transition audit trail (AC4): every guarded
// state change the engine recorded, oldest first. Steps are labelled by
// node_key resolved through the run's step list.

import type { WorkflowStep, WorkflowTransition } from "@multica/core/workflows/types";
import { useT, useTimeAgo } from "../../i18n";

export function StepTimeline({
  transitions,
  steps,
}: {
  transitions: WorkflowTransition[];
  steps: WorkflowStep[];
}) {
  const { t } = useT("workflows");
  const timeAgo = useTimeAgo();
  const nodeKeyByStepId = new Map(steps.map((s) => [s.id, s.node_key]));
  const sorted = [...transitions].sort((a, b) => a.created_at.localeCompare(b.created_at));

  if (sorted.length === 0) return null;

  return (
    <div className="flex flex-col gap-1">
      <h3 className="text-sm font-medium">{t(($) => $.run.timeline_title)}</h3>
      <ol className="flex flex-col border-l border-border pl-3">
        {sorted.map((tr) => (
          <li key={tr.id} className="flex flex-wrap items-baseline gap-2 py-1 text-xs">
            <span className="text-muted-foreground tabular-nums">
              {tr.created_at ? timeAgo(tr.created_at) : ""}
            </span>
            <span className="font-mono">{nodeKeyByStepId.get(tr.step_instance_id) ?? "?"}</span>
            <span>
              <span className="text-muted-foreground">{tr.from_status || "∅"}</span>
              {" → "}
              <span className="font-medium">{tr.to_status}</span>
            </span>
            <span className="text-muted-foreground tabular-nums">{`#${tr.attempt}`}</span>
            <span className="text-muted-foreground">
              {t(($) => $.run.trigger_by_label)}: {tr.trigger_by}
            </span>
          </li>
        ))}
      </ol>
    </div>
  );
}
