// workflow-events.ts — WS payload types for the workflow engine (P0 fork
// file). The two event names live in ./events.ts (the upstream union's +2
// touch); payload shapes live here so the fork's type surface stays out of
// the upstream file. Payloads are id-carriers only — consumers refetch the
// authoritative run/step through React Query rather than trusting wire data
// (same "WS as invalidation signal" contract as the rest of the app).

export interface WorkflowRunUpdatedPayload {
  run_id: string;
  // Optional hints the server may add later (status flip, acceptance
  // pending). Tolerate absence: the id alone is sufficient to invalidate.
  status?: string;
}

export interface WorkflowStepUpdatedPayload {
  run_id: string;
  step_instance_id: string;
  status?: string;
}
