"use client";

// template-list-page.tsx — /{slug}/workflows landing: the template catalog.
// Header actions jump to the sibling Runs / Hooks surfaces; rows navigate to
// the form-based template editor. Gated on the workflow_engine flag (the
// server 404s this API family while the flag is off, AC6).

import { AlertCircle, Play, Plus, Webhook, Workflow } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { useWorkflowEngineFlag } from "@multica/core/workflows/flag";
import { workflowTemplateListOptions } from "@multica/core/workflows/queries";
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
import { useState } from "react";
import { AppLink, useRowLink } from "../../navigation";
import {
  CollectionPageHeader,
  CollectionPageHeaderAction,
  CollectionPageState,
} from "../../layout/collection-page";
import { useT, useTimeAgo } from "../../i18n";
import { TemplateStatusBadge } from "./status-badges";
import { TemplateCreateDialog } from "./template-create-dialog";

export function TemplateListPage() {
  const { t } = useT("workflows");
  const timeAgo = useTimeAgo();
  const enabled = useWorkflowEngineFlag();
  const wsId = useWorkspaceId();
  const p = useWorkspacePaths();
  const rowLink = useRowLink();
  const [createOpen, setCreateOpen] = useState(false);
  const templatesQuery = useQuery({
    ...workflowTemplateListOptions(wsId),
    enabled,
  });

  if (!enabled) {
    return (
      <CollectionPageState
        icon={Workflow}
        title={t(($) => $.common.unavailable_title)}
        description={t(($) => $.common.unavailable_hint)}
      />
    );
  }

  const templates = templatesQuery.data ?? [];

  return (
    <div className="flex h-full flex-col">
      <CollectionPageHeader
        icon={Workflow}
        title={t(($) => $.templates.title)}
        count={templates.length}
        actions={
          <>
            <Button
              size="sm"
              variant="outline"
              render={<AppLink href={p.workflowRuns()} />}
            >
              <Play aria-hidden="true" className="size-3.5" />
              {t(($) => $.templates.runs_link)}
            </Button>
            <Button
              size="sm"
              variant="outline"
              render={<AppLink href={p.workflowHooks()} />}
            >
              <Webhook aria-hidden="true" className="size-3.5" />
              {t(($) => $.templates.hooks_link)}
            </Button>
            <CollectionPageHeaderAction
              icon={Plus}
              label={t(($) => $.templates.new_template)}
              onClick={() => setCreateOpen(true)}
            />
          </>
        }
      />

      {templatesQuery.isPending ? (
        <div className="flex flex-col gap-2 p-5">
          <Skeleton className="h-9 w-full" />
          <Skeleton className="h-9 w-full" />
          <Skeleton className="h-9 w-full" />
        </div>
      ) : templatesQuery.isError ? (
        <CollectionPageState
          icon={AlertCircle}
          tone="destructive"
          title={t(($) => $.common.error_title)}
          actions={
            <Button size="sm" variant="outline" onClick={() => templatesQuery.refetch()}>
              {t(($) => $.common.retry)}
            </Button>
          }
        />
      ) : templates.length === 0 ? (
        <CollectionPageState
          icon={Workflow}
          title={t(($) => $.templates.empty_title)}
          description={t(($) => $.templates.empty_hint)}
          actions={
            <Button size="sm" onClick={() => setCreateOpen(true)}>
              <Plus aria-hidden="true" className="size-3.5" />
              {t(($) => $.templates.new_template)}
            </Button>
          }
        />
      ) : (
        <div className="flex-1 overflow-auto">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t(($) => $.templates.table.key)}</TableHead>
                <TableHead>{t(($) => $.templates.table.name)}</TableHead>
                <TableHead>{t(($) => $.templates.table.version)}</TableHead>
                <TableHead>{t(($) => $.templates.table.status)}</TableHead>
                <TableHead>{t(($) => $.templates.table.updated)}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {templates.map((tmpl) => (
                <TableRow
                  key={tmpl.id}
                  className="cursor-pointer"
                  {...rowLink(p.workflowTemplateDetail(tmpl.id))}
                >
                  <TableCell className="font-mono text-xs">{tmpl.key}</TableCell>
                  <TableCell>{tmpl.name}</TableCell>
                  <TableCell className="tabular-nums">{`v${tmpl.version}`}</TableCell>
                  <TableCell>
                    <TemplateStatusBadge status={tmpl.status} />
                  </TableCell>
                  <TableCell className="text-muted-foreground">
                    {tmpl.updated_at ? timeAgo(tmpl.updated_at) : t(($) => $.common.none)}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}

      <TemplateCreateDialog open={createOpen} onOpenChange={setCreateOpen} />
    </div>
  );
}
