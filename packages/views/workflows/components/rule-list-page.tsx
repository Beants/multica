// rule-list-page.tsx — P1-fe-2 Rules asset management (P1-4 API). MVP: rule
// CRUD (create + list + delete). Binding management (target_type+target_id
// editor) lands in a follow-up — the API + mutations are ready
// (useCreateWorkflowRuleBinding/useDeleteWorkflowRuleBinding), the UI is the
// thin missing layer.

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";

import { workflowRuleListOptions } from "@multica/core/workflows/queries";
import {
  useCreateWorkflowRule,
  useDeleteWorkflowRule,
} from "@multica/core/workflows/mutations";
import type { WorkflowRuleLevel } from "@multica/core/workflows/types";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkflowEngineFlag } from "@multica/core/workflows/flag";

import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { useT } from "../../i18n";

const LEVEL_BADGE: Record<string, string> = {
  hard: "bg-destructive/10 text-destructive",
  safety: "bg-orange-500/10 text-orange-600",
  soft: "bg-muted text-muted-foreground",
};

const LEVELS: WorkflowRuleLevel[] = ["soft", "hard", "safety"];

export function RuleListPage() {
  const { t } = useT("workflows");
  const wsId = useWorkspaceId();
  const enabled = useWorkflowEngineFlag();
  const rulesQuery = useQuery({ ...workflowRuleListOptions(wsId), enabled });
  const createRule = useCreateWorkflowRule();
  const deleteRule = useDeleteWorkflowRule();

  const [name, setName] = useState("");
  const [level, setLevel] = useState<WorkflowRuleLevel>("soft");
  const [content, setContent] = useState("");

  const levelLabels: Record<string, string> = {
    soft: t(($) => $.rules.level.soft),
    hard: t(($) => $.rules.level.hard),
    safety: t(($) => $.rules.level.safety),
  };

  if (!enabled) {
    return (
      <div className="p-5 text-sm text-muted-foreground">
        {t(($) => $.rules.unavailable)}
      </div>
    );
  }

  if (rulesQuery.isPending) {
    return (
      <div className="flex flex-col gap-2 p-5">
        <Skeleton className="h-8 w-40" />
        <Skeleton className="h-24 w-full" />
      </div>
    );
  }

  const rules = rulesQuery.data ?? [];
  const canCreate = name.trim().length > 0 && content.trim().length > 0 && !createRule.isPending;

  const onCreate = () => {
    if (!canCreate) return;
    createRule.mutate(
      { name: name.trim(), level, content: content.trim() },
      {
        onSuccess: () => {
          setName("");
          setContent("");
        },
      },
    );
  };

  return (
    <div className="flex h-full flex-col gap-4 overflow-auto p-5">
      <h1 className="text-lg font-semibold">{t(($) => $.rules.title)}</h1>
      <p className="text-xs text-muted-foreground">
        {t(($) => $.rules.subtitle)}
      </p>

      <section className="flex flex-col gap-2 rounded-md border p-3">
        <h2 className="text-sm font-medium">{t(($) => $.rules.new_rule)}</h2>
        <div className="flex gap-2">
          <Input
            placeholder={t(($) => $.rules.name_placeholder)}
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
          <select
            className="rounded border bg-background px-2 text-sm"
            value={level}
            onChange={(e) => setLevel(e.target.value as WorkflowRuleLevel)}
          >
            {LEVELS.map((lv) => (
              <option key={lv} value={lv}>
                {levelLabels[lv] ?? lv}
              </option>
            ))}
          </select>
        </div>
        <textarea
          className="rounded border bg-background p-2 text-sm"
          rows={3}
          placeholder={t(($) => $.rules.content_placeholder)}
          value={content}
          onChange={(e) => setContent(e.target.value)}
        />
        <Button size="sm" className="self-start" disabled={!canCreate} onClick={onCreate}>
          {t(($) => $.rules.add_rule)}
        </Button>
        {createRule.isError && (
          <p className="text-xs text-destructive">{t(($) => $.rules.create_error)}</p>
        )}
      </section>

      <section className="flex flex-col gap-2">
        <h2 className="text-sm font-medium">
          {t(($) => $.rules.rule_count, { count: rules.length })}
        </h2>
        {rules.map((rule) => (
          <div key={rule.id} className="flex flex-col gap-1 rounded-md border p-3 text-xs">
            <div className="flex flex-wrap items-center gap-2">
              <span className="font-medium text-sm">{rule.name}</span>
              <span className={`rounded px-1.5 py-0.5 ${LEVEL_BADGE[rule.level] ?? LEVEL_BADGE.soft}`}>
                {levelLabels[rule.level] ?? rule.level}
              </span>
              <span className="text-muted-foreground">
                {t(($) => $.rules.scope_label, { scope: rule.scope })}
              </span>
              {rule.status !== "active" && (
                <span className="text-muted-foreground">({rule.status})</span>
              )}
              <Button
                size="sm"
                variant="ghost"
                className="ml-auto"
                disabled={deleteRule.isPending}
                onClick={() => deleteRule.mutate(rule.id)}
              >
                {t(($) => $.rules.delete)}
              </Button>
            </div>
            <p className="text-foreground">{rule.content}</p>
          </div>
        ))}
        {rules.length === 0 && (
          <p className="text-xs text-muted-foreground">{t(($) => $.rules.empty)}</p>
        )}
      </section>
    </div>
  );
}
