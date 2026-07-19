"use client";

// acceptance-panel.tsx — the run's acceptance decision surface (R5). The
// PENDING acceptance renders the operator controls: approve inline, or
// reject with a targeted rework node + reason (design.md §4.4: downstream
// steps are invalidated and the chain re-executes from the chosen node).
// Decided acceptances render as history.

import { useState } from "react";
import { toast } from "sonner";
import {
  useApproveAcceptance,
  useRejectAcceptance,
} from "@multica/core/workflows/mutations";
import type {
  WorkflowAcceptance,
  WorkflowRunDetail,
  WorkflowStep,
} from "@multica/core/workflows/types";
import { Button } from "@multica/ui/components/ui/button";
import { Label } from "@multica/ui/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { useT, useTimeAgo } from "../../i18n";
import { AcceptanceStatusBadge } from "./status-badges";

function DecidedAcceptance({ acceptance }: { acceptance: WorkflowAcceptance }) {
  const { t } = useT("workflows");
  const timeAgo = useTimeAgo();
  return (
    <div className="flex flex-col gap-1 rounded-lg border border-border p-3 text-sm">
      <div className="flex items-center gap-2">
        <AcceptanceStatusBadge status={acceptance.status} />
        {acceptance.decided_at != null && (
          <span className="text-xs text-muted-foreground">
            {t(($) => $.run.decided_at_label)}: {timeAgo(acceptance.decided_at)}
          </span>
        )}
      </div>
      {acceptance.status === "rejected" && (
        <>
          {acceptance.reject_to_node_key != null && (
            <p className="text-xs text-muted-foreground">
              {t(($) => $.run.reject_to_view)}:{" "}
              <span className="font-mono">{acceptance.reject_to_node_key}</span>
            </p>
          )}
          {acceptance.reject_reason != null && (
            <p className="text-xs">
              {t(($) => $.run.reject_reason_view)}: {acceptance.reject_reason}
            </p>
          )}
        </>
      )}
    </div>
  );
}

export function AcceptancePanel({
  run,
  steps,
}: {
  run: WorkflowRunDetail;
  steps: WorkflowStep[];
}) {
  const { t } = useT("workflows");
  const approveAcceptance = useApproveAcceptance();
  const rejectAcceptance = useRejectAcceptance();
  const [rejectOpen, setRejectOpen] = useState(false);
  const [rejectTo, setRejectTo] = useState("");
  const [reason, setReason] = useState("");

  const pending = run.acceptances.find((a) => a.status === "pending");
  const decided = run.acceptances.filter((a) => a.status !== "pending");

  // Rework targets = the nodes that already ran in this run (the server
  // rejects unknown targets with 400). Unique node_keys in step order.
  const targetOptions = [...new Set(steps.map((s) => s.node_key))].filter(
    (key) => key !== stepNodeKey(pending, steps),
  );
  const nodeNameByKey = new Map(
    run.template_snapshot?.nodes.map((n) => [n.node_key, n.name]) ?? [],
  );

  const approve = () => {
    if (approveAcceptance.isPending) return;
    approveAcceptance.mutate(run.id, {
      onSuccess: () => toast.success(t(($) => $.run.approve_success)),
      onError: (err) =>
        toast.error(err instanceof Error ? err.message : t(($) => $.run.approve_error)),
    });
  };

  const reject = () => {
    if (rejectAcceptance.isPending || rejectTo === "" || reason.trim() === "") return;
    rejectAcceptance.mutate(
      { runId: run.id, reject_to_node_key: rejectTo, reason: reason.trim() },
      {
        onSuccess: () => {
          setRejectOpen(false);
          setRejectTo("");
          setReason("");
          toast.success(t(($) => $.run.reject_success));
        },
        onError: (err) =>
          toast.error(err instanceof Error ? err.message : t(($) => $.run.reject_error)),
      },
    );
  };

  return (
    <div className="flex flex-col gap-2">
      <h3 className="text-sm font-medium">{t(($) => $.run.acceptance_title)}</h3>

      {pending && (
        <div className="flex flex-col gap-3 rounded-lg border border-primary/40 bg-muted/40 p-3">
          <div className="flex items-center gap-2">
            <AcceptanceStatusBadge status="pending" />
            <span className="text-xs text-muted-foreground">
              {t(($) => $.run.acceptance_pending_hint)}
            </span>
          </div>

          {!rejectOpen ? (
            <div className="flex items-center gap-2">
              <Button size="sm" onClick={approve} disabled={approveAcceptance.isPending}>
                {t(($) => $.run.approve)}
              </Button>
              <Button size="sm" variant="destructive" onClick={() => setRejectOpen(true)}>
                {t(($) => $.run.reject)}
              </Button>
            </div>
          ) : (
            <div className="flex flex-col gap-2">
              <div className="flex flex-col gap-1.5">
                <Label className="text-xs">{t(($) => $.run.reject_to_label)}</Label>
                <Select
                  items={targetOptions.map((key) => ({
                    value: key,
                    label: nodeNameByKey.get(key) ?? key,
                  }))}
                  value={rejectTo}
                  onValueChange={(v) => setRejectTo(v ?? "")}
                >
                  <SelectTrigger
                    size="sm"
                    className="w-full"
                    aria-label={t(($) => $.run.reject_to_label)}
                  >
                    <SelectValue>
                      {rejectTo === ""
                        ? t(($) => $.run.reject_to_placeholder)
                        : (nodeNameByKey.get(rejectTo) ?? rejectTo)}
                    </SelectValue>
                  </SelectTrigger>
                  <SelectContent>
                    {targetOptions.map((key) => (
                      <SelectItem key={key} value={key}>
                        {nodeNameByKey.get(key) ?? key}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
              <div className="flex flex-col gap-1.5">
                <Label htmlFor="wf-reject-reason" className="text-xs">
                  {t(($) => $.run.reject_reason_label)}
                </Label>
                <Textarea
                  id="wf-reject-reason"
                  value={reason}
                  onChange={(e) => setReason(e.target.value)}
                  placeholder={t(($) => $.run.reject_reason_placeholder)}
                  rows={3}
                />
              </div>
              <div className="flex items-center gap-2">
                <Button
                  size="sm"
                  variant="destructive"
                  onClick={reject}
                  disabled={
                    rejectAcceptance.isPending || rejectTo === "" || reason.trim() === ""
                  }
                >
                  {t(($) => $.run.reject_submit)}
                </Button>
                <Button size="sm" variant="ghost" onClick={() => setRejectOpen(false)}>
                  {t(($) => $.common.cancel)}
                </Button>
              </div>
            </div>
          )}
        </div>
      )}

      {decided.map((a) => (
        <DecidedAcceptance key={a.id} acceptance={a} />
      ))}
    </div>
  );
}

function stepNodeKey(
  acceptance: WorkflowAcceptance | undefined,
  steps: WorkflowStep[],
): string | null {
  if (!acceptance) return null;
  return steps.find((s) => s.id === acceptance.step_instance_id)?.node_key ?? null;
}
