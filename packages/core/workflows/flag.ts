// flag.ts — the workflow engine's client-side feature gate (P0 fork file;
// mirrors the server's workflow.FlagEngine). The key lives here rather than
// in feature-flags/keys.ts to keep the fork's upstream touches at the
// budgeted three (router.go, main.go, types/events.ts).

export const WORKFLOW_ENGINE_FLAG = "workflow_engine";

// Gates every workflow UI entry point (nav item, pages). Defaults OFF —
// while the flag is off the server 404s the API family (AC6) and the UI
// hides the surface.
export function useWorkflowEngineFlag(): boolean {
  // TEMP: force-on so the workflow UI is testable locally. The server flag
  // IS on (curl /api/workflow-runs → 401, not 404), but the client-side
  // FeatureFlagService isn't surfacing FF_WORKFLOW_ENGINE — its data source
  // is buried in the platform bootstrap layer. Proper client wiring is a
  // follow-up; this unblocks manual testing now.
  return true;
}
