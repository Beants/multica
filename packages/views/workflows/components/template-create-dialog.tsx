"use client";

// template-create-dialog.tsx — creates a draft template seeded with the
// minimal valid P0 chain (agent → acceptance → end): the server requires at
// least one node and every agent node a selector at create time, so the
// dialog collects the workspace agent the seeded agent node points at. The
// full node editor on the detail page takes it from there.

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { useCreateWorkflowTemplate } from "@multica/core/workflows/mutations";
import { agentListOptions } from "@multica/core/workspace/queries";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { useNavigation } from "../../navigation";
import { useT } from "../../i18n";

const EMPTY_AGENTS: { id: string; name: string }[] = [];

export function TemplateCreateDialog({
  open,
  onOpenChange,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const { t } = useT("workflows");
  const wsId = useWorkspaceId();
  const p = useWorkspacePaths();
  const { push } = useNavigation();
  const createTemplate = useCreateWorkflowTemplate();
  const { data: agents = EMPTY_AGENTS } = useQuery(agentListOptions(wsId));

  const [key, setKey] = useState("");
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [agentName, setAgentName] = useState("");

  const canSubmit = key.trim() !== "" && name.trim() !== "" && agentName !== "";

  const reset = () => {
    setKey("");
    setName("");
    setDescription("");
    setAgentName("");
  };

  const submit = () => {
    if (!canSubmit || createTemplate.isPending) return;
    createTemplate.mutate(
      {
        key: key.trim(),
        name: name.trim(),
        description: description.trim() || undefined,
        nodes: [
          {
            node_key: "implement",
            type: "agent",
            name: "Implementation",
            config: { role: "executor", agent_selector: agentName },
          },
          { node_key: "acceptance", type: "acceptance", name: "Acceptance" },
          { node_key: "end", type: "end", name: "End" },
        ],
        edges: [
          { from_node_key: "implement", to_node_key: "acceptance" },
          { from_node_key: "acceptance", to_node_key: "end" },
        ],
      },
      {
        onSuccess: (created) => {
          reset();
          onOpenChange(false);
          if (created.id) push(p.workflowTemplateDetail(created.id));
        },
        onError: (err) => {
          toast.error(err instanceof Error ? err.message : t(($) => $.create.error));
        },
      },
    );
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t(($) => $.create.title)}</DialogTitle>
          <DialogDescription>{t(($) => $.create.description)}</DialogDescription>
        </DialogHeader>
        <form
          className="flex flex-col gap-3"
          onSubmit={(e) => {
            e.preventDefault();
            submit();
          }}
        >
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="wf-create-key">{t(($) => $.create.key_label)}</Label>
            <Input
              id="wf-create-key"
              value={key}
              onChange={(e) => setKey(e.target.value)}
              placeholder={t(($) => $.create.key_placeholder)}
              autoFocus
            />
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="wf-create-name">{t(($) => $.create.name_label)}</Label>
            <Input
              id="wf-create-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder={t(($) => $.create.name_placeholder)}
            />
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="wf-create-desc">{t(($) => $.create.desc_label)}</Label>
            <Textarea
              id="wf-create-desc"
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder={t(($) => $.create.desc_placeholder)}
              rows={2}
            />
          </div>
          <div className="flex flex-col gap-1.5">
            <Label>{t(($) => $.detail.agent_selector_label)}</Label>
            <Select value={agentName} onValueChange={(v) => setAgentName(v ?? "")}>
              <SelectTrigger className="w-full" aria-label={t(($) => $.detail.agent_selector_label)}>
                <SelectValue>
                  {agentName || t(($) => $.detail.agent_selector_placeholder)}
                </SelectValue>
              </SelectTrigger>
              <SelectContent>
                {agents.map((agent) => (
                  <SelectItem key={agent.id} value={agent.name}>
                    {agent.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              {t(($) => $.common.cancel)}
            </Button>
            <Button type="submit" disabled={!canSubmit || createTemplate.isPending}>
              {t(($) => $.create.submit)}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
