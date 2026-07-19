// realtime.ts — WS → React Query wiring for workflow events (P0 fork file).
// Follows the per-page granular subscription pattern (useIssueTimeline):
// each workflow page subscribes while mounted instead of extending the
// global refreshMap in realtime/use-realtime-sync.ts (an upstream file the
// fork must not touch). Payloads are id-carriers — the handler invalidates
// and the refetch is authoritative.

import { useCallback } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useWSEvent } from "../realtime";
import type {
  WorkflowRunUpdatedPayload,
  WorkflowStepUpdatedPayload,
} from "../types/workflow-events";
import { workflowKeys } from "./queries";

// Subscribes to run-level workflow events for the runs list + open detail.
// Mount once per workflow page (list or detail).
export function useWorkflowRunsRealtime(wsId: string) {
  const qc = useQueryClient();
  const handler = useCallback(
    (payload: unknown) => {
      const p = payload as WorkflowRunUpdatedPayload | undefined;
      qc.invalidateQueries({ queryKey: workflowKeys.runs(wsId) });
      if (p?.run_id) {
        qc.invalidateQueries({ queryKey: workflowKeys.run(wsId, p.run_id) });
      }
    },
    [qc, wsId],
  );
  useWSEvent("workflow:run-updated", handler);
}

// Subscribes to step-level workflow events. A step transition always
// changes its run's detail payload (steps/submissions/verdicts/
// transitions), so the detail key is the invalidation target; the runs
// list only refetches on run-updated (a step update does not change the
// list row's status semantics — the run row flips on run events).
export function useWorkflowRunRealtime(wsId: string, runId: string) {
  const qc = useQueryClient();
  const handler = useCallback(
    (payload: unknown) => {
      const p = payload as WorkflowStepUpdatedPayload | undefined;
      if (p?.run_id && p.run_id !== runId) return;
      qc.invalidateQueries({ queryKey: workflowKeys.run(wsId, runId) });
    },
    [qc, wsId, runId],
  );
  useWSEvent("workflow:step-updated", handler);
}
