"use client";

// run-list-page.tsx — /{slug}/workflows/runs: every run in the workspace,
// newest first (server order). Runs waiting on an acceptance decision are
// highlighted — they are the operator's action queue.

import { AlertCircle, PlayCircle } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { useWorkflowEngineFlag } from "@multica/core/workflows/flag";
import {
  workflowRunListOptions,
  workflowTemplateListOptions,
} from "@multica/core/workflows/queries";
import { useWorkflowRunsRealtime } from "@multica/core/workflows/realtime";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { Button } from "@multica/ui/components/ui/button";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@multica/ui/components/ui/table";
import { cn } from "@multica/ui/lib/utils";
import { useRowLink } from "../../navigation";
import {
  CollectionPageHeader,
  CollectionPageState,
} from "../../layout/collection-page";
import { useT, useTimeAgo } from "../../i18n";
import { RunStatusBadge } from "./status-badges";

function shortId(id: string): string {
  return id.length > 8 ? id.slice(0, 8) : id;
}

export function RunListPage() {
  const { t } = useT("workflows");
  const timeAgo = useTimeAgo();
  const enabled = useWorkflowEngineFlag();
  const wsId = useWorkspaceId();
  const p = useWorkspacePaths();
  const rowLink = useRowLink();
  const runsQuery = useQuery({ ...workflowRunListOptions(wsId), enabled });
  const templatesQuery = useQuery({ ...workflowTemplateListOptions(wsId), enabled });
  useWorkflowRunsRealtime(wsId);

  if (!enabled) {
    return (
      <CollectionPageState
        icon={PlayCircle}
        title={t(($) => $.common.unavailable_title)}
        description={t(($) => $.common.unavailable_hint)}
      />
    );
  }

  const runs = runsQuery.data ?? [];
  const templateNameById = new Map(
    (templatesQuery.data ?? []).map((tmpl) => [tmpl.id, tmpl.name || tmpl.key]),
  );

  return (
    <div className="flex h-full flex-col">
      <CollectionPageHeader
        icon={PlayCircle}
        title={t(($) => $.runs.title)}
        count={runs.length}
      />

      {runsQuery.isPending ? (
        <div className="flex flex-col gap-2 p-5">
          <Skeleton className="h-9 w-full" />
          <Skeleton className="h-9 w-full" />
          <Skeleton className="h-9 w-full" />
        </div>
      ) : runsQuery.isError ? (
        <CollectionPageState
          icon={AlertCircle}
          tone="destructive"
          title={t(($) => $.common.error_title)}
          actions={
            <Button size="sm" variant="outline" onClick={() => runsQuery.refetch()}>
              {t(($) => $.common.retry)}
            </Button>
          }
        />
      ) : runs.length === 0 ? (
        <CollectionPageState
          icon={PlayCircle}
          title={t(($) => $.runs.empty_title)}
          description={t(($) => $.runs.empty_hint)}
        />
      ) : (
        <div className="flex-1 overflow-auto">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t(($) => $.runs.table.run)}</TableHead>
                <TableHead>{t(($) => $.runs.table.template)}</TableHead>
                <TableHead>{t(($) => $.runs.table.status)}</TableHead>
                <TableHead>{t(($) => $.runs.table.source)}</TableHead>
                <TableHead>{t(($) => $.runs.table.started)}</TableHead>
                <TableHead>{t(($) => $.runs.table.updated)}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {runs.map((run) => {
                const waiting = run.status === "waiting_acceptance";
                return (
                  <TableRow
                    key={run.id}
                    className={cn("cursor-pointer", waiting && "bg-muted/40")}
                    {...rowLink(p.workflowRunDetail(run.id))}
                  >
                    <TableCell className="font-mono text-xs">{shortId(run.id)}</TableCell>
                    <TableCell>
                      {templateNameById.get(run.template_id) ?? t(($) => $.common.none)}
                    </TableCell>
                    <TableCell>
                      <RunStatusBadge status={run.status} />
                    </TableCell>
                    <TableCell className="text-muted-foreground">
                      {run.source_id
                        ? `${run.source_type}:${run.source_id}`
                        : run.source_type || t(($) => $.common.none)}
                    </TableCell>
                    <TableCell className="text-muted-foreground">
                      {run.started_at ? timeAgo(run.started_at) : t(($) => $.common.none)}
                    </TableCell>
                    <TableCell className="text-muted-foreground">
                      {run.updated_at ? timeAgo(run.updated_at) : t(($) => $.common.none)}
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        </div>
      )}
    </div>
  );
}
