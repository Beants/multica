"use client";

// status-badges.tsx — shared status pills for the workflow pages. Server
// statuses are lenient strings (zod keeps z.string()): every label map has
// an explicit default branch that renders the RAW status rather than
// crashing or blanking (CLAUDE.md: server-driven enum switches need a
// default branch). Badge variants come from the design system — no new
// colors.

import { Badge } from "@multica/ui/components/ui/badge";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";

type BadgeVariant = "default" | "secondary" | "destructive" | "outline" | "ghost";

function statusVariant(map: Record<string, BadgeVariant>, status: string): BadgeVariant {
  return map[status] ?? "outline";
}

export function RunStatusBadge({ status, className }: { status: string; className?: string }) {
  const { t } = useT("workflows");
  const labels: Record<string, string> = {
    running: t(($) => $.runs.status.running),
    paused: t(($) => $.runs.status.paused),
    waiting_acceptance: t(($) => $.runs.status.waiting_acceptance),
    completed: t(($) => $.runs.status.completed),
    failed: t(($) => $.runs.status.failed),
    cancelled: t(($) => $.runs.status.cancelled),
  };
  // waiting_acceptance is the operator call-to-action — brand-filled so it
  // stands out in list rows (pending-acceptance highlight).
  const variants: Record<string, BadgeVariant> = {
    running: "default",
    waiting_acceptance: "default",
    paused: "outline",
    completed: "secondary",
    failed: "destructive",
    cancelled: "ghost",
  };
  return (
    <Badge variant={statusVariant(variants, status)} className={className}>
      {labels[status] ?? status}
    </Badge>
  );
}

export function StepStatusBadge({ status, className }: { status: string; className?: string }) {
  const { t } = useT("workflows");
  const labels: Record<string, string> = {
    pending: t(($) => $.run.step_status.pending),
    active: t(($) => $.run.step_status.active),
    dispatched: t(($) => $.run.step_status.dispatched),
    running: t(($) => $.run.step_status.running),
    passed: t(($) => $.run.step_status.passed),
    failed: t(($) => $.run.step_status.failed),
    blocked: t(($) => $.run.step_status.blocked),
    rework: t(($) => $.run.step_status.rework),
    skipped: t(($) => $.run.step_status.skipped),
  };
  const variants: Record<string, BadgeVariant> = {
    active: "default",
    dispatched: "default",
    running: "default",
    passed: "secondary",
    failed: "destructive",
    blocked: "destructive",
    rework: "outline",
    pending: "ghost",
    skipped: "ghost",
  };
  return (
    <Badge variant={statusVariant(variants, status)} className={className}>
      {labels[status] ?? status}
    </Badge>
  );
}

export function TemplateStatusBadge({ status, className }: { status: string; className?: string }) {
  const { t } = useT("workflows");
  const labels: Record<string, string> = {
    draft: t(($) => $.templates.status.draft),
    published: t(($) => $.templates.status.published),
    archived: t(($) => $.templates.status.archived),
  };
  const variants: Record<string, BadgeVariant> = {
    draft: "outline",
    published: "default",
    archived: "ghost",
  };
  return (
    <Badge variant={statusVariant(variants, status)} className={className}>
      {labels[status] ?? status}
    </Badge>
  );
}

export function HookStatusBadge({ status, className }: { status: string; className?: string }) {
  const { t } = useT("workflows");
  const labels: Record<string, string> = {
    active: t(($) => $.hooks.status.active),
    disabled: t(($) => $.hooks.status.disabled),
  };
  const variants: Record<string, BadgeVariant> = {
    active: "default",
    disabled: "ghost",
  };
  return (
    <Badge variant={statusVariant(variants, status)} className={className}>
      {labels[status] ?? status}
    </Badge>
  );
}

export function SubmissionStatusBadge({ status, className }: { status: string; className?: string }) {
  const { t } = useT("workflows");
  const labels: Record<string, string> = {
    DONE: t(($) => $.run.submission_status.DONE),
    DONE_WITH_CONCERNS: t(($) => $.run.submission_status.DONE_WITH_CONCERNS),
    BLOCKED: t(($) => $.run.submission_status.BLOCKED),
    NEEDS_CONTEXT: t(($) => $.run.submission_status.NEEDS_CONTEXT),
  };
  const variants: Record<string, BadgeVariant> = {
    DONE: "secondary",
    DONE_WITH_CONCERNS: "outline",
    BLOCKED: "destructive",
    NEEDS_CONTEXT: "outline",
  };
  return (
    <Badge variant={statusVariant(variants, status)} className={className}>
      {labels[status] ?? status}
    </Badge>
  );
}

export function VerdictResultBadge({ result, className }: { result: string; className?: string }) {
  const { t } = useT("workflows");
  const labels: Record<string, string> = {
    pass: t(($) => $.run.verdict_result.pass),
    fail: t(($) => $.run.verdict_result.fail),
    blocked: t(($) => $.run.verdict_result.blocked),
  };
  const variants: Record<string, BadgeVariant> = {
    pass: "secondary",
    fail: "destructive",
    blocked: "destructive",
  };
  return (
    <Badge variant={statusVariant(variants, result)} className={className}>
      {labels[result] ?? result}
    </Badge>
  );
}

export function AcceptanceStatusBadge({ status, className }: { status: string; className?: string }) {
  const { t } = useT("workflows");
  const labels: Record<string, string> = {
    pending: t(($) => $.run.acceptance_status.pending),
    approved: t(($) => $.run.acceptance_status.approved),
    rejected: t(($) => $.run.acceptance_status.rejected),
  };
  const variants: Record<string, BadgeVariant> = {
    pending: "default",
    approved: "secondary",
    rejected: "destructive",
  };
  return (
    <Badge variant={statusVariant(variants, status)} className={cn(className)}>
      {labels[status] ?? status}
    </Badge>
  );
}
