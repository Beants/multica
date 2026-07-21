/* eslint-disable i18next/no-literal-string */
// capability-editor.tsx — P1-fe-3 agent capability labels (P1-7 dispatch
// matcher data). Inline list + add (upsert by key) + delete. Mounted on the
// agent detail surface so operators can label proficiency per capability_key;
// the workflow dispatch matcher (MatchAgentByCapability) consumes these rows.
//
// Hardcoded English; parent plan bundles i18n across fe-1/2/3.

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";

import { agentCapabilityListOptions } from "@multica/core/workflows/queries";
import {
  useCreateAgentCapability,
  useDeleteAgentCapability,
} from "@multica/core/workflows/mutations";
import { useWorkspaceId } from "@multica/core/hooks";

import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";

export function CapabilityEditor({ agentId, enabled = true }: { agentId: string; enabled?: boolean }) {
  const wsId = useWorkspaceId();
  const capsQuery = useQuery({ ...agentCapabilityListOptions(wsId, agentId), enabled });
  const createCap = useCreateAgentCapability(agentId);
  const deleteCap = useDeleteAgentCapability(agentId);

  const [key, setKey] = useState("");
  const [proficiency, setProficiency] = useState(50);

  if (!enabled) return null;

  const caps = capsQuery.data ?? [];
  const canCreate = key.trim().length > 0 && proficiency >= 0 && proficiency <= 100 && !createCap.isPending;

  const onAdd = () => {
    if (!canCreate) return;
    createCap.mutate(
      { capability_key: key.trim(), proficiency },
      { onSuccess: () => setKey("") },
    );
  };

  return (
    <section className="flex flex-col gap-2">
      <h3 className="text-sm font-medium">Capabilities</h3>
      <p className="text-xs text-muted-foreground">
        Labels + proficiency consumed by the workflow capability matcher (P1-7). A node declaring <code>required_capabilities</code> routes to the most proficient agent.
      </p>

      <div className="flex gap-2">
        <Input
          placeholder="capability_key (e.g. python)"
          value={key}
          onChange={(e) => setKey(e.target.value)}
        />
        <input
          type="number"
          min={0}
          max={100}
          className="w-20 rounded border bg-background px-2 text-sm"
          value={proficiency}
          onChange={(e) => setProficiency(Number(e.target.value))}
        />
        <Button size="sm" disabled={!canCreate} onClick={onAdd}>
          Add
        </Button>
      </div>

      {caps.map((c) => (
        <div key={c.id} className="flex items-center gap-2 rounded-md border p-2 text-xs">
          <span className="font-medium">{c.capability_key}</span>
          <span className="text-muted-foreground">proficiency {c.proficiency}</span>
          <Button
            size="sm"
            variant="ghost"
            className="ml-auto"
            disabled={deleteCap.isPending}
            onClick={() => deleteCap.mutate(c.id)}
          >
            Delete
          </Button>
        </div>
      ))}
      {caps.length === 0 && (
        <p className="text-xs text-muted-foreground">No capabilities labeled yet.</p>
      )}
    </section>
  );
}
