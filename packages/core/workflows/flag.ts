// flag.ts — the workflow engine's client-side feature gate (P0 fork file;
// mirrors the server's workflow.FlagEngine). The key lives here rather than
// in feature-flags/keys.ts to keep the fork's upstream touches at the
// budgeted three (router.go, main.go, types/events.ts).

import { useFlag } from "../feature-flags";

export const WORKFLOW_ENGINE_FLAG = "workflow_engine";

// Gates every workflow UI entry point (nav item, pages). Defaults OFF —
// while the flag is off the server 404s the API family (AC6) and the UI
// hides the surface.
export function useWorkflowEngineFlag(): boolean {
  return useFlag(WORKFLOW_ENGINE_FLAG, false);
}
