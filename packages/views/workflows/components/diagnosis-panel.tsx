import { useQuery } from "@tanstack/react-query";

import { workflowRunDiagnosisOptions } from "@multica/core/workflows/queries";
import type { StepDiagnosis } from "@multica/core/workflows/types";

// diagnosis-panel.tsx — P1-fe-1 run diagnosis view. Renders the seven-element
// per-step diagnosis (军团文 §4.5) from GET /api/workflow-runs/{id}/diagnosis.
// Failed steps (ok=false) expand; a clean run collapses to a one-line "all OK".
//
// i18n: intentionally hardcoded English for the MVP (the parent plan lists
// locale keys as a follow-up across all three fe-* tasks so the glossary lands
// coherently rather than piecemeal).

export function DiagnosisPanel({
  wsId,
  runId,
  enabled,
}: {
  wsId: string;
  runId: string;
  enabled: boolean;
}) {
  const { data, isPending } = useQuery({
    ...workflowRunDiagnosisOptions(wsId, runId),
    enabled,
  });

  if (!enabled || isPending || !data) {
    return null;
  }

  const failed = data.steps.filter((s) => !s.ok);
  if (failed.length === 0) {
    return (
      <section className="flex flex-col gap-1">
        <h3 className="text-sm font-medium">Diagnosis</h3>
        <p className="text-xs text-muted-foreground">
          No failed steps — all {data.steps.length} step(s) OK.
        </p>
      </section>
    );
  }

  return (
    <section className="flex flex-col gap-2">
      <h3 className="text-sm font-medium">
        Diagnosis · {failed.length} failed step(s)
      </h3>
      {failed.map((s) => (
        <DiagnosisCard key={s.step_id} step={s} />
      ))}
    </section>
  );
}

function DiagnosisCard({ step }: { step: StepDiagnosis }) {
  // The transition timeline is summarized inline — a verbose timeline already
  // lives in StepTimeline on the same page; here we only surface the count +
  // the from→to arc so the operator sees the retry history shape at a glance.
  const arc = step.transitions
    .map((tr) => `${tr.from_status || "—"}→${tr.to_status}`)
    .join(", ");

  // gate_run.output / agent_task.result (七要素 ⑤): surface stderr if present.
  let stderr = "";
  if (step.output && typeof step.output === "object") {
    const raw = (step.output as Record<string, unknown>).stderr;
    if (typeof raw === "string") stderr = raw;
  }

  return (
    <div className="flex flex-col gap-1 rounded-md border p-3 text-xs">
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-mono font-medium">{step.node_key || step.step_id.slice(0, 8)}</span>
        <span className="rounded bg-destructive/10 px-1.5 py-0.5 font-medium text-destructive">
          {step.failure_type || step.final_status}
        </span>
        <span className="text-muted-foreground">
          attempt {step.attempt}
          {step.max_attempts ? `/${step.max_attempts}` : ""}
        </span>
        {step.agent_id && (
          <span className="text-muted-foreground">agent {step.agent_id.slice(0, 8)}</span>
        )}
      </div>
      {step.reason && <p className="text-foreground">{step.reason}</p>}
      {stderr && (
        <pre className="max-h-32 overflow-auto rounded bg-muted p-2 font-mono text-[10px]">
          {stderr}
        </pre>
      )}
      {arc && <p className="text-muted-foreground">{step.transitions.length} transition(s): {arc}</p>}
    </div>
  );
}
