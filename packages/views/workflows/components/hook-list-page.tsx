"use client";

// hook-list-page.tsx — /{slug}/workflows/hooks: the inbound hook registry.
// Tokens are never listed (the server stores only hashes); management here
// is create (token shown once) + disable.

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { AlertCircle, Plus, Webhook } from "lucide-react";
import { toast } from "sonner";
import { useWorkflowEngineFlag } from "@multica/core/workflows/flag";
import {
  workflowHookListOptions,
  workflowTemplateListOptions,
} from "@multica/core/workflows/queries";
import { useDisableWorkflowHook } from "@multica/core/workflows/mutations";
import { useWorkspaceId } from "@multica/core/hooks";
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
import {
  CollectionPageHeader,
  CollectionPageHeaderAction,
  CollectionPageState,
} from "../../layout/collection-page";
import { useT, useTimeAgo } from "../../i18n";
import { CreateHookDialog } from "./create-hook-dialog";
import { HookStatusBadge } from "./status-badges";

export function HookListPage() {
  const { t } = useT("workflows");
  const timeAgo = useTimeAgo();
  const enabled = useWorkflowEngineFlag();
  const wsId = useWorkspaceId();
  const [createOpen, setCreateOpen] = useState(false);
  const hooksQuery = useQuery({ ...workflowHookListOptions(wsId), enabled });
  const templatesQuery = useQuery({ ...workflowTemplateListOptions(wsId), enabled });
  const disableHook = useDisableWorkflowHook();

  if (!enabled) {
    return (
      <CollectionPageState
        icon={Webhook}
        title={t(($) => $.common.unavailable_title)}
        description={t(($) => $.common.unavailable_hint)}
      />
    );
  }

  const hooks = hooksQuery.data ?? [];
  const templateNameById = new Map(
    (templatesQuery.data ?? []).map((tmpl) => [tmpl.id, tmpl.name || tmpl.key]),
  );

  const disable = (id: string) => {
    if (disableHook.isPending) return;
    disableHook.mutate(id, {
      onSuccess: () => toast.success(t(($) => $.hooks.disable_success)),
      onError: (err) =>
        toast.error(err instanceof Error ? err.message : t(($) => $.hooks.disable_error)),
    });
  };

  return (
    <div className="flex h-full flex-col">
      <CollectionPageHeader
        icon={Webhook}
        title={t(($) => $.hooks.title)}
        count={hooks.length}
        actions={
          <CollectionPageHeaderAction
            icon={Plus}
            label={t(($) => $.hooks.new_hook)}
            onClick={() => setCreateOpen(true)}
          />
        }
      />

      {hooksQuery.isPending ? (
        <div className="flex flex-col gap-2 p-5">
          <Skeleton className="h-9 w-full" />
          <Skeleton className="h-9 w-full" />
        </div>
      ) : hooksQuery.isError ? (
        <CollectionPageState
          icon={AlertCircle}
          tone="destructive"
          title={t(($) => $.common.error_title)}
          actions={
            <Button size="sm" variant="outline" onClick={() => hooksQuery.refetch()}>
              {t(($) => $.common.retry)}
            </Button>
          }
        />
      ) : hooks.length === 0 ? (
        <CollectionPageState
          icon={Webhook}
          title={t(($) => $.hooks.empty_title)}
          description={t(($) => $.hooks.empty_hint)}
          actions={
            <Button size="sm" onClick={() => setCreateOpen(true)}>
              <Plus aria-hidden="true" className="size-3.5" />
              {t(($) => $.hooks.new_hook)}
            </Button>
          }
        />
      ) : (
        <div className="flex-1 overflow-auto">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t(($) => $.hooks.table.name)}</TableHead>
                <TableHead>{t(($) => $.hooks.table.template)}</TableHead>
                <TableHead>{t(($) => $.hooks.table.status)}</TableHead>
                <TableHead>{t(($) => $.hooks.table.last_used)}</TableHead>
                <TableHead>{t(($) => $.hooks.table.created)}</TableHead>
                <TableHead>{t(($) => $.hooks.table.actions)}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {hooks.map((hook) => (
                <TableRow key={hook.id}>
                  <TableCell>{hook.name}</TableCell>
                  <TableCell>
                    {templateNameById.get(hook.template_id) ?? t(($) => $.common.none)}
                  </TableCell>
                  <TableCell>
                    <HookStatusBadge status={hook.status} />
                  </TableCell>
                  <TableCell className="text-muted-foreground">
                    {hook.last_used_at != null ? timeAgo(hook.last_used_at) : t(($) => $.common.none)}
                  </TableCell>
                  <TableCell className="text-muted-foreground">
                    {hook.created_at ? timeAgo(hook.created_at) : t(($) => $.common.none)}
                  </TableCell>
                  <TableCell>
                    {hook.status === "active" && (
                      <Button
                        size="xs"
                        variant="ghost"
                        onClick={() => disable(hook.id)}
                        disabled={disableHook.isPending}
                      >
                        {t(($) => $.hooks.disable)}
                      </Button>
                    )}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}

      <CreateHookDialog open={createOpen} onOpenChange={setCreateOpen} />
    </div>
  );
}
