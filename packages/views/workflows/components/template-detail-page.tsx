"use client";

// template-detail-page.tsx — /{slug}/workflows/templates/{id}: the template
// editor surface. P0/P1 shipped a form-only chain editor; P3-2 adds a
// read-only node canvas (React Flow) as a sibling Tab so operators can
// visualize complex graphs. Drafts edit name/description + the ordered
// node list inline on the Form tab; publish freezes the graph
// server-side; published/archived templates render read-only (the server
// enforces the same rule with 409).

import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { AlertCircle, AlertTriangle, Archive, Plus, Workflow } from "lucide-react";
import { toast } from "sonner";
import { workflowTemplateDetailOptions } from "@multica/core/workflows/queries";
import {
  useArchiveWorkflowTemplate,
  usePublishWorkflowTemplate,
  useUpdateWorkflowTemplate,
} from "@multica/core/workflows/mutations";
import { useWorkflowEngineFlag } from "@multica/core/workflows/flag";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import type {
  WorkflowNodeInput,
  WorkflowTemplateDetail,
} from "@multica/core/workflows/types";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@multica/ui/components/ui/tabs";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { BreadcrumbHeader } from "../../layout/breadcrumb-header";
import { CollectionPageState } from "../../layout/collection-page";
import { useT } from "../../i18n";
import { TemplateStatusBadge } from "./status-badges";
import { NodeEditorCard, emptyNode, newExitField, type EditableNode } from "./node-editor";
import { WorkflowCanvas } from "./workflow-canvas";

// Orders the node list by walking the edge chain from the head (the node
// with no incoming edge). P0 chains are linear; a malformed/broken chain
// falls back to the server's array order so nothing is silently dropped.
function orderNodes(detail: WorkflowTemplateDetail) {
  const nodes = detail.nodes;
  if (nodes.length === 0 || detail.edges.length === 0) return nodes;
  const hasIncoming = new Set(detail.edges.map((e) => e.to_node_key));
  const nextByFrom = new Map(detail.edges.map((e) => [e.from_node_key, e.to_node_key]));
  const head = nodes.find((n) => !hasIncoming.has(n.node_key));
  if (!head) return nodes;
  const byKey = new Map(nodes.map((n) => [n.node_key, n]));
  const ordered = [];
  let cursor: string | undefined = head.node_key;
  const seen = new Set<string>();
  while (cursor !== undefined && !seen.has(cursor)) {
    seen.add(cursor);
    const node = byKey.get(cursor);
    if (!node) break;
    ordered.push(node);
    cursor = nextByFrom.get(cursor);
  }
  return ordered.length === nodes.length ? ordered : nodes;
}

// True when the server graph is NOT the linear chain this form edits:
// multiple heads, a branch (out-degree > 1), a cycle that swallows nodes,
// or any conditional edge. Saving a non-linear graph from the list editor
// would silently rewrite it as a linear chain — the banner warns first.
function isNonLinearGraph(detail: WorkflowTemplateDetail): boolean {
  const { nodes, edges } = detail;
  if (nodes.length === 0) return false;
  if (edges.some((e) => e.condition != null)) return true;
  if (edges.length === 0) return nodes.length > 1;
  const hasIncoming = new Set(edges.map((e) => e.to_node_key));
  if (nodes.filter((n) => !hasIncoming.has(n.node_key)).length !== 1) return true;
  const outDegree = new Map<string, number>();
  for (const e of edges) outDegree.set(e.from_node_key, (outDegree.get(e.from_node_key) ?? 0) + 1);
  if ([...outDegree.values()].some((d) => d > 1)) return true;
  // A well-formed linear chain visits every node walking from the head.
  const walked = orderNodes(detail);
  return walked !== nodes && walked.length !== nodes.length;
}

function toEditable(detail: WorkflowTemplateDetail): EditableNode[] {
  return orderNodes(detail).map((n) => {
    const cfg = n.config ?? {};
    return {
      node_key: n.node_key,
      type: n.type,
      name: n.name,
      role: cfg.role ?? "executor",
      agent_selector: cfg.agent_selector ?? cfg.agent_id ?? "",
      instructions: cfg.instructions ?? "",
      max_attempts: cfg.max_attempts ?? 0,
      auto_pass: cfg.auto_pass === true,
      exit_fields: (cfg.exit_fields?.fields ?? []).map((f) => ({
        ...newExitField(),
        name: f.name,
        type: f.type || "any",
        required: f.required === true,
        description: f.description ?? "",
      })),
    };
  });
}

// Derives the linear edge list from list order (P0: condition-less default
// edges). The server requires nodes+edges together on a graph rewrite.
function deriveEdges(nodes: EditableNode[]) {
  return nodes.slice(0, -1).map((n, i) => ({
    from_node_key: n.node_key,
    to_node_key: nodes[i + 1]!.node_key,
  }));
}

// Client-side mirror of the server's create/update graph validation, so the
// obvious 400s surface as a toast before the round-trip.
function validateNodes(nodes: EditableNode[]): string | null {
  if (nodes.length === 0) return "at least one node is required";
  const keys = new Set<string>();
  for (const n of nodes) {
    if (n.node_key.trim() === "") return "node_key is required";
    if (keys.has(n.node_key)) return `duplicate node_key "${n.node_key}"`;
    keys.add(n.node_key);
    if (n.name.trim() === "") return `node "${n.node_key}" requires a name`;
    if (n.type === "agent" && n.agent_selector.trim() === "")
      return `node "${n.node_key}" requires an agent selector`;
    const fieldNames = new Set<string>();
    for (const f of n.exit_fields) {
      if (f.name.trim() === "") return `node "${n.node_key}" has an exit field with an empty name`;
      if (fieldNames.has(f.name.trim()))
        return `node "${n.node_key}" has a duplicate exit field "${f.name.trim()}"`;
      fieldNames.add(f.name.trim());
    }
  }
  return null;
}

function toNodeInputs(nodes: EditableNode[]): WorkflowNodeInput[] {
  return nodes.map((n) => {
    const isAgent = n.type === "agent";
    const fields = n.exit_fields.filter((f) => f.name.trim() !== "");
    return {
      node_key: n.node_key.trim(),
      type: n.type,
      name: n.name.trim(),
      config: {
        ...(isAgent ? { role: n.role as "executor" | "evaluator" | "reviewer" } : {}),
        ...(isAgent && n.agent_selector.trim() !== ""
          ? { agent_selector: n.agent_selector.trim() }
          : {}),
        ...(isAgent && n.instructions.trim() !== "" ? { instructions: n.instructions } : {}),
        ...(isAgent && n.max_attempts > 0 ? { max_attempts: n.max_attempts } : {}),
        ...(n.type === "acceptance" && n.auto_pass === true ? { auto_pass: true } : {}),
        ...(fields.length > 0
          ? {
              exit_fields: {
                fields: fields.map((f) => ({
                  name: f.name.trim(),
                  type: f.type,
                  ...(f.required === true ? { required: true } : {}),
                  ...(f.description.trim() !== "" ? { description: f.description.trim() } : {}),
                })),
              },
            }
          : {}),
      },
    };
  });
}

// Read-only node summary for published/archived templates.
function NodeSummary({ node, index }: { node: EditableNode; index: number }) {
  const { t } = useT("workflows");
  const typeLabels: Record<string, string> = {
    agent: t(($) => $.detail.node_type_agent),
    acceptance: t(($) => $.detail.node_type_acceptance),
    end: t(($) => $.detail.node_type_end),
  };
  const roleLabels: Record<string, string> = {
    executor: t(($) => $.detail.role_executor),
    evaluator: t(($) => $.detail.role_evaluator),
    reviewer: t(($) => $.detail.role_reviewer),
  };
  return (
    <div className="flex flex-col gap-1 rounded-lg border border-border p-3">
      <div className="flex items-center gap-2 text-sm">
        <span className="text-muted-foreground tabular-nums">{index + 1}.</span>
        <span className="font-mono text-xs">{node.node_key}</span>
        <span className="font-medium">{node.name}</span>
        <span className="text-xs text-muted-foreground">{typeLabels[node.type] ?? node.type}</span>
        {node.type === "agent" && (
          <span className="text-xs text-muted-foreground">
            {roleLabels[node.role] ?? node.role} · {node.agent_selector}
          </span>
        )}
      </div>
      {node.instructions !== "" && (
        <p className="text-xs text-muted-foreground">{node.instructions}</p>
      )}
      {node.exit_fields.length > 0 && (
        <p className="font-mono text-xs text-muted-foreground">
          {node.exit_fields
            .map((f) => `${f.name}${f.required === true ? "*" : ""}: ${f.type}`)
            .join(", ")}
        </p>
      )}
    </div>
  );
}

// Stable structural snapshot for dirty detection — reference equality won't
// do because every keystroke rebuilds the nodes array.
function snapshotForm(name: string, description: string, nodes: EditableNode[]): string {
  return JSON.stringify({ name, description, nodes });
}

function TemplateEditor({
  template,
  onDirtyChange,
}: {
  template: WorkflowTemplateDetail;
  onDirtyChange: (dirty: boolean) => void;
}) {
  const { t } = useT("workflows");
  const updateTemplate = useUpdateWorkflowTemplate();
  const publishTemplate = usePublishWorkflowTemplate();

  const [name, setName] = useState(template.name);
  const [description, setDescription] = useState(template.description);
  const [nodes, setNodes] = useState<EditableNode[]>(() => toEditable(template));

  const baseline = useMemo(
    () => snapshotForm(template.name, template.description, toEditable(template)),
    [template],
  );
  const dirty = snapshotForm(name, description, nodes) !== baseline;

  // Lift dirty state to the page header + warn on tab close. The remount
  // key on <TemplateEditor> (id:updated_at) re-seeds the baseline after a
  // save/publish refetch, which resets dirty automatically.
  useEffect(() => {
    onDirtyChange(dirty);
    if (!dirty) return;
    const warnBeforeUnload = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      event.returnValue = "";
    };
    window.addEventListener("beforeunload", warnBeforeUnload);
    return () => window.removeEventListener("beforeunload", warnBeforeUnload);
  }, [dirty, onDirtyChange]);

  const patchNode = (index: number, next: EditableNode) =>
    setNodes((prev) => prev.map((n, i) => (i === index ? next : n)));
  const moveNode = (index: number, delta: -1 | 1) =>
    setNodes((prev) => {
      const target = index + delta;
      if (target < 0 || target >= prev.length) return prev;
      const next = [...prev];
      [next[index], next[target]] = [next[target]!, next[index]!];
      return next;
    });
  const removeNode = (index: number) =>
    setNodes((prev) => prev.filter((_, i) => i !== index));

  const save = () => {
    if (updateTemplate.isPending) return;
    const invalid = validateNodes(nodes);
    if (invalid !== null) {
      toast.error(invalid);
      return;
    }
    updateTemplate.mutate(
      {
        id: template.id,
        name: name.trim(),
        description: description.trim(),
        nodes: toNodeInputs(nodes),
        edges: deriveEdges(nodes),
      },
      {
        onSuccess: () => toast.success(t(($) => $.detail.save_success)),
        onError: (err) =>
          toast.error(err instanceof Error ? err.message : t(($) => $.detail.save_error)),
      },
    );
  };

  const publish = () => {
    if (publishTemplate.isPending) return;
    publishTemplate.mutate(template.id, {
      onSuccess: () => toast.success(t(($) => $.detail.publish_success)),
      onError: (err) =>
        toast.error(err instanceof Error ? err.message : t(($) => $.detail.publish_error)),
    });
  };

  const nonLinear = isNonLinearGraph(template);

  // P3-2: form (default) + canvas tabs coexist. The canvas is read-only —
  // editing still happens on the form tab. defaultValue="form" keeps the
  // existing tests (which query form fields without clicking a tab) green.
  return (
    <Tabs
      defaultValue="form"
      className="flex flex-1 flex-col gap-0 overflow-hidden"
    >
      <div className="flex shrink-0 items-center gap-1 border-b border-border px-3 pt-2">
        <TabsList variant="line">
          <TabsTrigger value="form">
            {t(($) => $.canvas.tab_form)}
          </TabsTrigger>
          <TabsTrigger value="canvas">
            {t(($) => $.canvas.tab_canvas)}
          </TabsTrigger>
        </TabsList>
        {nonLinear && (
          <span className="ml-2 inline-flex items-center gap-1 text-xs text-orange-600 dark:text-orange-400">
            <AlertTriangle aria-hidden="true" className="size-3.5" />
            {t(($) => $.canvas.nonlinear_badge)}
          </span>
        )}
      </div>
      <TabsContent
        value="form"
        className="mt-0 flex flex-1 flex-col overflow-hidden"
      >
        <div className="flex flex-1 flex-col gap-4 overflow-auto p-5">
          {nonLinear && (
            <div
              role="alert"
              className="flex items-start gap-2 rounded-lg border border-orange-500/40 bg-orange-500/10 p-3 text-xs text-orange-600 dark:text-orange-400"
            >
              <AlertTriangle aria-hidden="true" className="mt-0.5 size-3.5 shrink-0" />
              <p>{t(($) => $.detail.nonlinear_warning)}</p>
            </div>
          )}
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="wf-tpl-name">{t(($) => $.detail.name_label)}</Label>
            <Input id="wf-tpl-name" value={name} onChange={(e) => setName(e.target.value)} />
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="wf-tpl-desc">{t(($) => $.detail.desc_label)}</Label>
            <Textarea
              id="wf-tpl-desc"
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              rows={2}
            />
          </div>

          <div className="flex flex-col gap-2">
            <div className="flex items-center justify-between">
              <div>
                <h2 className="text-sm font-medium">{t(($) => $.detail.nodes_title)}</h2>
                <p className="text-xs text-muted-foreground">{t(($) => $.detail.nodes_hint)}</p>
              </div>
              <Button
                size="sm"
                variant="outline"
                onClick={() => setNodes((prev) => [...prev, emptyNode(prev.length)])}
              >
                <Plus aria-hidden="true" className="size-3.5" />
                {t(($) => $.detail.add_node)}
              </Button>
            </div>
            {nodes.map((node, i) => (
              <NodeEditorCard
                key={`${i}-${node.node_key}`}
                node={node}
                index={i}
                total={nodes.length}
                onChange={(next) => patchNode(i, next)}
                onMove={moveNode}
                onRemove={removeNode}
              />
            ))}
          </div>

          <div className="flex items-center gap-2">
            <Button
              size="sm"
              onClick={save}
              disabled={updateTemplate.isPending || name.trim() === "" || !dirty}
            >
              {t(($) => $.detail.save)}
            </Button>
            <Button
              size="sm"
              variant="outline"
              onClick={publish}
              disabled={publishTemplate.isPending}
            >
              {t(($) => $.detail.publish)}
            </Button>
            {dirty && (
              <span className="text-xs text-muted-foreground">
                {t(($) => $.detail.unsaved_changes)}
              </span>
            )}
          </div>
        </div>
      </TabsContent>
      <TabsContent
        value="canvas"
        className="mt-0 flex-1 overflow-hidden"
      >
        <WorkflowCanvas template={template} />
      </TabsContent>
    </Tabs>
  );
}

function TemplateReadonly({ template }: { template: WorkflowTemplateDetail }) {
  const { t } = useT("workflows");
  const nodes = toEditable(template);
  return (
    <Tabs
      defaultValue="summary"
      className="flex flex-1 flex-col gap-0 overflow-hidden"
    >
      <div className="flex shrink-0 items-center gap-1 border-b border-border px-3 pt-2">
        <TabsList variant="line">
          <TabsTrigger value="summary">
            {t(($) => $.canvas.tab_summary)}
          </TabsTrigger>
          <TabsTrigger value="canvas">
            {t(($) => $.canvas.tab_canvas)}
          </TabsTrigger>
        </TabsList>
      </div>
      <TabsContent
        value="summary"
        className="mt-0 flex flex-1 flex-col overflow-auto p-5"
      >
        <p className="text-xs text-muted-foreground">
          {template.status === "archived"
            ? t(($) => $.detail.readonly_archived_hint)
            : t(($) => $.detail.readonly_published_hint)}
        </p>
        {template.description !== "" && (
          <p className="text-sm text-muted-foreground">{template.description}</p>
        )}
        <div className="flex flex-col gap-2">
          <h2 className="text-sm font-medium">{t(($) => $.detail.nodes_title)}</h2>
          {nodes.map((node, i) => (
            <NodeSummary key={`${i}-${node.node_key}`} node={node} index={i} />
          ))}
        </div>
      </TabsContent>
      <TabsContent
        value="canvas"
        className="mt-0 flex-1 overflow-hidden"
      >
        <WorkflowCanvas template={template} />
      </TabsContent>
    </Tabs>
  );
}

export function TemplateDetailPage({ templateId }: { templateId: string }) {
  const { t } = useT("workflows");
  const enabled = useWorkflowEngineFlag();
  const wsId = useWorkspaceId();
  const p = useWorkspacePaths();
  const archiveTemplate = useArchiveWorkflowTemplate();
  const [archiveOpen, setArchiveOpen] = useState(false);
  const [editorDirty, setEditorDirty] = useState(false);
  const detailQuery = useQuery({
    ...workflowTemplateDetailOptions(wsId, templateId),
    enabled,
  });

  const archive = () => {
    archiveTemplate.mutate(templateId, {
      onSuccess: () => {
        setArchiveOpen(false);
        toast.success(t(($) => $.detail.archive_success));
      },
      onError: (err) =>
        toast.error(err instanceof Error ? err.message : t(($) => $.detail.archive_error)),
    });
  };

  if (!enabled) {
    return (
      <CollectionPageState
        icon={Workflow}
        title={t(($) => $.common.unavailable_title)}
        description={t(($) => $.common.unavailable_hint)}
      />
    );
  }

  if (detailQuery.isPending) {
    return (
      <div className="flex flex-col gap-2 p-5">
        <Skeleton className="h-8 w-64" />
        <Skeleton className="h-24 w-full" />
        <Skeleton className="h-24 w-full" />
      </div>
    );
  }

  if (detailQuery.isError || !detailQuery.data?.id) {
    return (
      <CollectionPageState
        icon={AlertCircle}
        tone="destructive"
        title={t(($) => $.detail.not_found_title)}
        description={t(($) => $.detail.not_found_hint)}
      />
    );
  }

  const template = detailQuery.data;
  const readonly = template.status !== "draft";

  return (
    <div className="flex h-full flex-col">
      <BreadcrumbHeader
        segments={[{ href: p.workflows(), label: t(($) => $.detail.breadcrumb_templates) }]}
        leaf={
          <span className="flex items-center gap-2">
            <span className="truncate">{template.name || t(($) => $.detail.unnamed_template)}</span>
            <TemplateStatusBadge status={template.status} />
            <span className="font-mono text-xs text-muted-foreground">{`v${template.version}`}</span>
            {editorDirty && (
              <span className="text-xs font-normal text-muted-foreground">
                {t(($) => $.detail.unsaved_changes)}
              </span>
            )}
          </span>
        }
        actions={
          template.status !== "archived" ? (
            <Button size="sm" variant="outline" onClick={() => setArchiveOpen(true)}>
              <Archive aria-hidden="true" className="size-3.5" />
              {t(($) => $.detail.archive)}
            </Button>
          ) : undefined
        }
      />
      {/* Remount the editor when the server row changes (save/publish
          refetch bumps updated_at) so local form state re-seeds from the
          authoritative payload instead of drifting. */}
      {readonly ? (
        <TemplateReadonly template={template} />
      ) : (
        <TemplateEditor
          key={`${template.id}:${template.updated_at}`}
          template={template}
          onDirtyChange={setEditorDirty}
        />
      )}

      <AlertDialog open={archiveOpen} onOpenChange={setArchiveOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t(($) => $.detail.archive_confirm_title)}</AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.detail.archive_confirm_body)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t(($) => $.common.cancel)}</AlertDialogCancel>
            <AlertDialogAction onClick={archive} disabled={archiveTemplate.isPending}>
              {t(($) => $.detail.archive_confirm_action)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
