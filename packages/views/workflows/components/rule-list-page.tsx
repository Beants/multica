/* eslint-disable i18next/no-literal-string */
// rule-list-page.tsx — P1-fe-2 Rules asset management (P1-4 API). MVP: rule
// CRUD (create + list + delete). Binding management (target_type+target_id
// editor) lands in a follow-up — the API + mutations are ready
// (useCreateWorkflowRuleBinding/useDeleteWorkflowRuleBinding), the UI is the
// thin missing layer.
//
// Hardcoded English; the parent plan (07-21-p1-fe-completion) bundles i18n
// keys across fe-1/2/3 in one coherent glossary pass.

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

const LEVEL_BADGE: Record<string, string> = {
  hard: "bg-destructive/10 text-destructive",
  safety: "bg-orange-500/10 text-orange-600",
  soft: "bg-muted text-muted-foreground",
};

export function RuleListPage() {
  const wsId = useWorkspaceId();
  const enabled = useWorkflowEngineFlag();
  const rulesQuery = useQuery({ ...workflowRuleListOptions(wsId), enabled });
  const createRule = useCreateWorkflowRule();
  const deleteRule = useDeleteWorkflowRule();

  const [name, setName] = useState("");
  const [level, setLevel] = useState<WorkflowRuleLevel>("soft");
  const [content, setContent] = useState("");

  if (!enabled) {
    return (
      <div className="p-5 text-sm text-muted-foreground">
        Workflow engine is off — Rules are unavailable.
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
      <h1 className="text-lg font-semibold">Rules</h1>
      <p className="text-xs text-muted-foreground">
        Team constraints injected into agent context (soft) or enforced at gates (hard). Bindings to nodes/agents land in a follow-up.
      </p>

      <section className="flex flex-col gap-2 rounded-md border p-3">
        <h2 className="text-sm font-medium">New rule</h2>
        <div className="flex gap-2">
          <Input
            placeholder="Name (e.g. PR must include test coverage)"
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
          <select
            className="rounded border bg-background px-2 text-sm"
            value={level}
            onChange={(e) => setLevel(e.target.value as WorkflowRuleLevel)}
          >
            <option value="soft">soft</option>
            <option value="hard">hard</option>
            <option value="safety">safety</option>
          </select>
        </div>
        <textarea
          className="rounded border bg-background p-2 text-sm"
          rows={3}
          placeholder="Rule content — the constraint text agents see (soft) or gates check (hard)"
          value={content}
          onChange={(e) => setContent(e.target.value)}
        />
        <Button size="sm" className="self-start" disabled={!canCreate} onClick={onCreate}>
          Add rule
        </Button>
        {createRule.isError && (
          <p className="text-xs text-destructive">Failed to create rule.</p>
        )}
      </section>

      <section className="flex flex-col gap-2">
        <h2 className="text-sm font-medium">
          {rules.length} rule(s)
        </h2>
        {rules.map((rule) => (
          <div key={rule.id} className="flex flex-col gap-1 rounded-md border p-3 text-xs">
            <div className="flex flex-wrap items-center gap-2">
              <span className="font-medium text-sm">{rule.name}</span>
              <span className={`rounded px-1.5 py-0.5 ${LEVEL_BADGE[rule.level] ?? LEVEL_BADGE.soft}`}>
                {rule.level}
              </span>
              <span className="text-muted-foreground">scope: {rule.scope}</span>
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
                Delete
              </Button>
            </div>
            <p className="text-foreground">{rule.content}</p>
          </div>
        ))}
        {rules.length === 0 && (
          <p className="text-xs text-muted-foreground">No rules yet — create one above.</p>
        )}
      </section>
    </div>
  );
}
