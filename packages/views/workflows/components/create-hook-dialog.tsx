"use client";

// create-hook-dialog.tsx — mints an inbound workflow hook. The cleartext
// token comes back exactly once (the server stores only its SHA-256 hash),
// so the dialog swaps to a token reveal view after creation with an
// explicit copy affordance; closing that view is the point of no return.

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Check, Copy } from "lucide-react";
import { toast } from "sonner";
import { useCreateWorkflowHook } from "@multica/core/workflows/mutations";
import { workflowTemplateListOptions } from "@multica/core/workflows/queries";
import { useWorkspaceId } from "@multica/core/hooks";
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
import { copyText } from "@multica/ui/lib/clipboard";
import { useT } from "../../i18n";

export function CreateHookDialog({
  open,
  onOpenChange,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const { t } = useT("workflows");
  const wsId = useWorkspaceId();
  const createHook = useCreateWorkflowHook();
  const { data: templates = [] } = useQuery(workflowTemplateListOptions(wsId));
  const published = templates.filter((tmpl) => tmpl.status === "published");

  const [name, setName] = useState("");
  const [templateId, setTemplateId] = useState("");
  const [token, setToken] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);

  const templateNameById = new Map(templates.map((tmpl) => [tmpl.id, tmpl.name || tmpl.key]));
  const canSubmit = name.trim() !== "" && templateId !== "";

  const close = (next: boolean) => {
    if (!next) {
      setName("");
      setTemplateId("");
      setToken(null);
      setCopied(false);
    }
    onOpenChange(next);
  };

  const submit = () => {
    if (!canSubmit || createHook.isPending) return;
    createHook.mutate(
      { name: name.trim(), template_id: templateId },
      {
        onSuccess: (created) => setToken(created.token),
        onError: (err) =>
          toast.error(err instanceof Error ? err.message : t(($) => $.hooks.create_error)),
      },
    );
  };

  const copy = async () => {
    if (token === null) return;
    await copyText(token);
    setCopied(true);
  };

  return (
    <Dialog open={open} onOpenChange={close}>
      <DialogContent>
        {token === null ? (
          <>
            <DialogHeader>
              <DialogTitle>{t(($) => $.hooks.create_title)}</DialogTitle>
              <DialogDescription>{t(($) => $.hooks.create_description)}</DialogDescription>
            </DialogHeader>
            <form
              className="flex flex-col gap-3"
              onSubmit={(e) => {
                e.preventDefault();
                submit();
              }}
            >
              <div className="flex flex-col gap-1.5">
                <Label htmlFor="wf-hook-name">{t(($) => $.hooks.name_label)}</Label>
                <Input
                  id="wf-hook-name"
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  placeholder={t(($) => $.hooks.name_placeholder)}
                  autoFocus
                />
              </div>
              <div className="flex flex-col gap-1.5">
                <Label>{t(($) => $.hooks.template_label)}</Label>
                <Select
                  items={published.map((tmpl) => ({
                    value: tmpl.id,
                    label: `${tmpl.name || tmpl.key} (v${tmpl.version})`,
                  }))}
                  value={templateId}
                  onValueChange={(v) => setTemplateId(v ?? "")}
                >
                  <SelectTrigger className="w-full" aria-label={t(($) => $.hooks.template_label)}>
                    <SelectValue>
                      {templateId === ""
                        ? t(($) => $.hooks.template_placeholder)
                        : (templateNameById.get(templateId) ?? templateId)}
                    </SelectValue>
                  </SelectTrigger>
                  <SelectContent>
                    {published.map((tmpl) => (
                      <SelectItem key={tmpl.id} value={tmpl.id}>
                        {`${tmpl.name || tmpl.key} (v${tmpl.version})`}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
              <DialogFooter>
                <Button type="button" variant="outline" onClick={() => close(false)}>
                  {t(($) => $.common.cancel)}
                </Button>
                <Button type="submit" disabled={!canSubmit || createHook.isPending}>
                  {t(($) => $.hooks.submit)}
                </Button>
              </DialogFooter>
            </form>
          </>
        ) : (
          <>
            <DialogHeader>
              <DialogTitle>{t(($) => $.hooks.token_title)}</DialogTitle>
              <DialogDescription>{t(($) => $.hooks.token_hint)}</DialogDescription>
            </DialogHeader>
            <div className="flex items-center gap-2">
              <code className="flex-1 overflow-x-auto rounded-md bg-muted/50 p-2 font-mono text-xs whitespace-nowrap">
                {token}
              </code>
              <Button
                size="icon-sm"
                variant="outline"
                aria-label={t(($) => $.common.copy)}
                onClick={copy}
              >
                {copied ? <Check aria-hidden="true" /> : <Copy aria-hidden="true" />}
              </Button>
            </div>
            <p className="text-xs text-muted-foreground">
              {t(($) => $.hooks.endpoint_hint)}{" "}
              <span className="font-mono">{`/api/hooks/workflow/${token}`}</span>
            </p>
            <DialogFooter>
              <Button onClick={() => close(false)}>{t(($) => $.common.close)}</Button>
            </DialogFooter>
          </>
        )}
      </DialogContent>
    </Dialog>
  );
}
