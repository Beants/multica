"use client";

// run-detail-page.tsx — /{slug}/workflows/runs/{id}: the AC4 trace view.
// Composition: header meta → acceptance decision surface (pending
// highlighted) → per-step cards (snapshot-ordered; submission + verdict
// inline) → the step_transition timeline.

import { useQuery } from "@tanstack/react-query";
import { AlertCircle, PlayCircle } from "lucide-react";
import { workflowRunDetailOptions } from "@multica/core/workflows/queries";
import { useWorkflowRunRealtime, useWorkflowRunsRealtime } from "@multica/core/workflows/realtime";
import { useWorkflowEngineFlag } from "@multica/core/workflows/flag";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import type { WorkflowRunDetail, WorkflowStep } from "@multica/core/workflows/types";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { BreadcrumbHeader } from "../../layout/breadcrumb-header";
import { CollectionPageState } from "../../layout/collection-page";
import { useT, useTimeAgo } from "../../i18n";
import { AcceptancePanel } from "./acceptance-panel";
import { RunStatusBadge, StepStatusBadge } from "./status-badges";
import { StepTimeline } from "./step-timeline";
import { DiagnosisPanel } from "./diagnosis-panel";
import { SubmissionView } from "./submission-view";
import { VerdictView } from "./verdict-view";

// Orders steps by the frozen snapshot chain (walk edges from the head),
// attempts grouped under their node. Steps whose node_key is absent from
// the snapshot (forward-compat drift) append at the end in server order.
function orderSteps(run: WorkflowRunDetail): WorkflowStep[] {
  const snap = run.template_snapshot;
  const steps = run.steps;
  if (!snap || snap.nodes.length === 0) return steps;
  const chainKeys: string[] = [];
  const hasIncoming = new Set(snap.edges.map((e) => e.to_node_key));
  const nextByFrom = new Map(snap.edges.map((e) => [e.from_node_key, e.to_node_key]));
  const head = snap.nodes.find((n) => !hasIncoming.has(n.node_key));
  if (head) {
    let cursor: string | undefined = head.node_key;
    const seen = new Set<string>();
    while (cursor !== undefined && !seen.has(cursor)) {
      seen.add(cursor);
      chainKeys.push(cursor);
      cursor = nextByFrom.get(cursor);
    }
  }
  const byKey = new Map<string, WorkflowStep[]>();
  for (const s of steps) {
    const list = byKey.get(s.node_key) ?? [];
    list.push(s);
    byKey.set(s.node_key, list);
  }
  const ordered: WorkflowStep[] = [];
  for (const key of chainKeys) {
    const group = byKey.get(key) ?? [];
    ordered.push(...group.sort((a, b) => a.attempt - b.attempt));
    byKey.delete(key);
  }
  for (const group of byKey.values()) ordered.push(...group);
  return ordered;
}

function StepCard({ run, step }: { run: WorkflowRunDetail; step: WorkflowStep }) {
  const { t } = useT("workflows");
  const snapNode = run.template_snapshot?.nodes.find((n) => n.node_key === step.node_key);
  const typeLabels: Record<string, string> = {
    agent: t(($) => $.run.node_type_agent),
    acceptance: t(($) => $.run.node_type_acceptance),
    end: t(($) => $.run.node_type_end),
  };
  const submission = run.submissions.find((s) => s.step_instance_id === step.id);
  const verdict = run.verdicts.find((v) => v.step_instance_id === step.id);

  return (
    <div className="flex flex-col gap-3 rounded-lg border border-border p-3">
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-medium">{snapNode?.name ?? step.node_key}</span>
        <span className="font-mono text-xs text-muted-foreground">{step.node_key}</span>
        {snapNode && (
          <span className="text-xs text-muted-foreground">
            {typeLabels[snapNode.type] ?? snapNode.type}
          </span>
        )}
        <StepStatusBadge status={step.status} />
        <span className="text-xs text-muted-foreground tabular-nums">{`#${step.attempt}`}</span>
      </div>
      {submission ? (
        <SubmissionView submission={submission} />
      ) : (
        <p className="text-xs text-muted-foreground">{t(($) => $.run.no_submission)}</p>
      )}
      {verdict ? (
        <VerdictView verdict={verdict} />
      ) : (
        <p className="text-xs text-muted-foreground">{t(($) => $.run.no_verdict)}</p>
      )}
    </div>
  );
}

export function RunDetailPage({ runId }: { runId: string }) {
  const { t } = useT("workflows");
  const timeAgo = useTimeAgo();
  const enabled = useWorkflowEngineFlag();
  const wsId = useWorkspaceId();
  const p = useWorkspacePaths();
  const runQuery = useQuery({ ...workflowRunDetailOptions(wsId, runId), enabled });
  useWorkflowRunsRealtime(wsId);
  useWorkflowRunRealtime(wsId, runId);

  if (!enabled) {
    return (
      <CollectionPageState
        icon={PlayCircle}
        title={t(($) => $.common.unavailable_title)}
        description={t(($) => $.common.unavailable_hint)}
      />
    );
  }

  if (runQuery.isPending) {
    return (
      <div className="flex flex-col gap-2 p-5">
        <Skeleton className="h-8 w-64" />
        <Skeleton className="h-24 w-full" />
        <Skeleton className="h-24 w-full" />
      </div>
    );
  }

  if (runQuery.isError || !runQuery.data?.id) {
    return (
      <CollectionPageState
        icon={AlertCircle}
        tone="destructive"
        title={t(($) => $.run.not_found_title)}
        description={t(($) => $.run.not_found_hint)}
      />
    );
  }

  const run = runQuery.data;
  const steps = orderSteps(run);
  const snap = run.template_snapshot;
  const shortId = run.id.length > 8 ? run.id.slice(0, 8) : run.id;

  return (
    <div className="flex h-full flex-col">
      <BreadcrumbHeader
        segments={[{ href: p.workflowRuns(), label: t(($) => $.run.breadcrumb_runs) }]}
        leaf={
          <span className="flex items-center gap-2">
            <span className="font-mono text-xs">{shortId}</span>
            <RunStatusBadge status={run.status} />
          </span>
        }
      />

      <div className="flex flex-1 flex-col gap-4 overflow-auto p-5">
        <dl className="flex flex-wrap gap-x-6 gap-y-1 text-xs text-muted-foreground">
          <div className="flex gap-1">
            <dt>{t(($) => $.run.template_label)}:</dt>
            <dd className="text-foreground">
              {snap?.key ? `${snap.key} v${snap.version}` : t(($) => $.common.none)}
            </dd>
          </div>
          <div className="flex gap-1">
            <dt>{t(($) => $.run.source_label)}:</dt>
            <dd className="text-foreground">
              {run.source_id ? `${run.source_type}:${run.source_id}` : run.source_type}
            </dd>
          </div>
          <div className="flex gap-1">
            <dt>{t(($) => $.run.started_label)}:</dt>
            <dd className="text-foreground">
              {run.started_at ? timeAgo(run.started_at) : t(($) => $.common.none)}
            </dd>
          </div>
          {run.completed_at != null && (
            <div className="flex gap-1">
              <dt>{t(($) => $.run.completed_label)}:</dt>
              <dd className="text-foreground">{timeAgo(run.completed_at)}</dd>
            </div>
          )}
        </dl>

        {run.acceptances.length > 0 && <AcceptancePanel run={run} steps={steps} />}

        <div className="flex flex-col gap-2">
          <h3 className="text-sm font-medium">{t(($) => $.run.steps_title)}</h3>
          {steps.map((step) => (
            <StepCard key={step.id} run={run} step={step} />
          ))}
        </div>

        <StepTimeline transitions={run.transitions} steps={steps} />

        <DiagnosisPanel wsId={wsId} runId={runId} enabled={enabled} />
      </div>
    </div>
  );
}
