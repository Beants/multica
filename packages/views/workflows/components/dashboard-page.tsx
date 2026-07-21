/* eslint-disable i18next/no-literal-string */
// dashboard-page.tsx — P2-4 workflow observability. MVP: scene-layer event
// distribution (bar) + recent events feed. Consumes P2-1 (events) + P2-3
// (metrics) APIs. Hardcoded English (i18n bundled across fe/P2 later).

import { useQuery } from "@tanstack/react-query";

import { workflowMetricsOptions, workflowEventsOptions } from "@multica/core/workflows/queries";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkflowEngineFlag } from "@multica/core/workflows/flag";

import { Skeleton } from "@multica/ui/components/ui/skeleton";

export function DashboardPage() {
  const wsId = useWorkspaceId();
  const enabled = useWorkflowEngineFlag();
  const metricsQ = useQuery({ ...workflowMetricsOptions(wsId), enabled });
  const eventsQ = useQuery({ ...workflowEventsOptions(wsId), enabled });

  if (!enabled) {
    return <div className="p-5 text-sm text-muted-foreground">Workflow engine is off.</div>;
  }

  const metrics = metricsQ.data ?? [];
  const events = eventsQ.data ?? [];
  const maxCount = metrics.reduce((m, r) => Math.max(m, r.event_count), 0) || 1;

  return (
    <div className="flex h-full flex-col gap-4 overflow-auto p-5">
      <h1 className="text-lg font-semibold">Workflow observability</h1>

      <section className="flex flex-col gap-2">
        <h2 className="text-sm font-medium">Event distribution</h2>
        {metricsQ.isPending ? (
          <Skeleton className="h-8 w-full" />
        ) : metrics.length === 0 ? (
          <p className="text-xs text-muted-foreground">No events yet.</p>
        ) : (
          metrics.map((m) => (
            <div key={m.event_type} className="flex items-center gap-2 text-xs">
              <span className="w-48 shrink-0 truncate font-mono">{m.event_type}</span>
              <div className="h-4 flex-1 rounded bg-muted">
                <div
                  className="h-4 rounded bg-brand"
                  style={{ width: `${(m.event_count / maxCount) * 100}%` }}
                />
              </div>
              <span className="w-12 shrink-0 text-right tabular-nums">{m.event_count}</span>
            </div>
          ))
        )}
      </section>

      <section className="flex flex-col gap-2">
        <h2 className="text-sm font-medium">Recent events ({events.length})</h2>
        <div className="flex flex-col gap-1">
          {events.slice(0, 30).map((e) => (
            <div key={e.id} className="flex items-center gap-2 rounded-md border p-2 text-xs">
              <span className="font-mono text-muted-foreground">
                {e.occurred_at ? new Date(e.occurred_at).toLocaleTimeString() : ""}
              </span>
              <span className="font-medium">{e.event_type}</span>
              {e.actor_type && (
                <span className="text-muted-foreground">by {e.actor_type}</span>
              )}
            </div>
          ))}
          {events.length === 0 && <p className="text-xs text-muted-foreground">No events.</p>}
        </div>
      </section>
    </div>
  );
}
