"use client";

// submission-view.tsx — one step's submission: self-declared status, raw
// summary, exit fields, artifacts (persistent refs — PR URLs / branches /
// attachment ids), and gaps (D-2).

import type { WorkflowSubmission } from "@multica/core/workflows/types";
import { useT } from "../../i18n";
import { JsonBlock } from "./json-block";
import { SubmissionStatusBadge } from "./status-badges";

function hasContent(value: unknown): boolean {
  if (value === null || value === undefined) return false;
  if (Array.isArray(value)) return value.length > 0;
  if (typeof value === "object") return Object.keys(value).length > 0;
  return true;
}

export function SubmissionView({ submission }: { submission: WorkflowSubmission }) {
  const { t } = useT("workflows");
  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center gap-2">
        <h4 className="text-xs font-medium text-muted-foreground">
          {t(($) => $.run.submission_title)}
        </h4>
        <SubmissionStatusBadge status={submission.status} />
      </div>
      {submission.raw_summary != null && submission.raw_summary !== "" && (
        <div>
          <p className="text-xs font-medium text-muted-foreground">
            {t(($) => $.run.summary_title)}
          </p>
          <p className="text-sm whitespace-pre-wrap">{submission.raw_summary}</p>
        </div>
      )}
      {hasContent(submission.exit_fields) && (
        <div>
          <p className="text-xs font-medium text-muted-foreground">
            {t(($) => $.run.exit_fields_title)}
          </p>
          <JsonBlock value={submission.exit_fields} />
        </div>
      )}
      {hasContent(submission.artifacts) && (
        <div>
          <p className="text-xs font-medium text-muted-foreground">
            {t(($) => $.run.artifacts_title)}
          </p>
          <JsonBlock value={submission.artifacts} />
        </div>
      )}
      {hasContent(submission.gaps) && (
        <div>
          <p className="text-xs font-medium text-muted-foreground">
            {t(($) => $.run.gaps_title)}
          </p>
          <JsonBlock value={submission.gaps} />
        </div>
      )}
    </div>
  );
}
