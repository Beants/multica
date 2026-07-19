"use client";

// verdict-view.tsx — one step's verdict: result, root cause, confidence,
// verdict_by (system/agent/human — the verdict actor model), and evidence.

import type { WorkflowVerdict } from "@multica/core/workflows/types";
import { useT } from "../../i18n";
import { JsonBlock } from "./json-block";
import { VerdictResultBadge } from "./status-badges";

export function VerdictView({ verdict }: { verdict: WorkflowVerdict }) {
  const { t } = useT("workflows");
  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center gap-2">
        <h4 className="text-xs font-medium text-muted-foreground">
          {t(($) => $.run.verdict_title)}
        </h4>
        <VerdictResultBadge result={verdict.result} />
        <span className="text-xs text-muted-foreground">
          {t(($) => $.run.verdict_by_label)}: {verdict.verdict_by}
        </span>
        {verdict.confidence != null && (
          <span className="text-xs text-muted-foreground tabular-nums">
            {t(($) => $.run.confidence_label)}: {verdict.confidence}
          </span>
        )}
      </div>
      {verdict.root_cause != null && verdict.root_cause !== "" && (
        <p className="text-sm">
          <span className="text-xs font-medium text-muted-foreground">
            {t(($) => $.run.root_cause_label)}:{" "}
          </span>
          {verdict.root_cause}
        </p>
      )}
      {verdict.evidence != null && (
        <div>
          <p className="text-xs font-medium text-muted-foreground">
            {t(($) => $.run.evidence_title)}
          </p>
          <JsonBlock value={verdict.evidence} />
        </div>
      )}
    </div>
  );
}
