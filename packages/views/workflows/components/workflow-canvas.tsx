"use client";

// workflow-canvas.tsx — P3-2 MVP node canvas for a workflow template
// (React Flow). The existing form editor (node-editor.tsx) only edits a
// linear chain; once templates grow past ~5 nodes the form gets cramped.
// This canvas READS the same template payload and visualizes the graph:
// nodes colored by type (semantic tokens, no new palette), edges from
// from_node_key→to_node_key, drag-to-rearrange for layout exploration,
// and a read-only details panel on the right when a node is selected.
//
// MVP scope (per P3-2 prd):
//   - Visualize + read-only inspect. No node/edge create/delete.
//   - Positions are LOCAL state only. Template has no persisted position
//     field wired through the API yet; drag is for the operator's eyes.
//   - Node config editing stays in the Form tab.
//
// Follow-ups (out of MVP): node/edge CRUD + serialization, position
// persistence, conditional-edge labels, mini-map polish, keyboard
// shortcuts.

import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react";
import {
  Background,
  BackgroundVariant,
  Controls,
  Handle,
  MarkerType,
  Position,
  ReactFlow,
  useEdgesState,
  useNodesState,
  type Edge,
  type Node,
  type NodeMouseHandler,
  type NodeProps,
} from "@xyflow/react";
import { Bot, Flag, ShieldCheck, Workflow as WorkflowIcon } from "lucide-react";
import "@xyflow/react/dist/style.css";
import type {
  WorkflowTemplateDetail,
  WorkflowTemplateEdge,
  WorkflowTemplateNode,
} from "@multica/core/workflows/types";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";

// ---------------------------------------------------------------------------
// Layout
// ---------------------------------------------------------------------------

// Sized to fit the node card content (key + name + type label). Keep in
// sync with the inline className widths on CanvasNode below.
const NODE_W = 220;
const NODE_H = 96;
const COLUMN_GAP = 64;
const ROW_GAP = 32;

// Layered auto-layout: depth[v] = max(depth[u] for u in preds[v]) + 1.
// Heads (no incoming edge) sit at column 0; subsequent columns follow the
// longest path. Within a column, nodes stack top-to-bottom and the column
// is centered vertically against the tallest column. This renders the
// linear P0 chain top-to-bottom AND a branching P1-2 graph without a
// third-party layout dep. Kahn's algorithm protects against cycles (the
// server payload is trusted-lenient, not trusted-correct) by falling
// back to declared array order in one column if any node is left over.
function computeLayeredLayout(
  nodes: WorkflowTemplateNode[],
  edges: WorkflowTemplateEdge[],
): Map<string, { x: number; y: number }> {
  const preds = new Map<string, string[]>();
  const succs = new Map<string, string[]>();
  for (const n of nodes) {
    preds.set(n.node_key, []);
    succs.set(n.node_key, []);
  }
  for (const e of edges) {
    preds.get(e.to_node_key)?.push(e.from_node_key);
    succs.get(e.from_node_key)?.push(e.to_node_key);
  }
  const indeg = new Map<string, number>();
  const depth = new Map<string, number>();
  for (const n of nodes) {
    indeg.set(n.node_key, preds.get(n.node_key)!.length);
    depth.set(n.node_key, 0);
  }
  const queue: string[] = nodes
    .filter((n) => indeg.get(n.node_key) === 0)
    .map((n) => n.node_key);
  let visited = 0;
  while (queue.length > 0) {
    const key = queue.shift()!;
    visited += 1;
    const d = depth.get(key) ?? 0;
    for (const next of succs.get(key) ?? []) {
      depth.set(next, Math.max(depth.get(next) ?? 0, d + 1));
      const newIn = (indeg.get(next) ?? 1) - 1;
      indeg.set(next, newIn);
      if (newIn === 0) queue.push(next);
    }
  }
  const out = new Map<string, { x: number; y: number }>();
  if (visited < nodes.length) {
    // Cycle guard: lay the remaining nodes out in declared order along a
    // single column so nothing is silently dropped.
    nodes.forEach((n, i) =>
      out.set(n.node_key, { x: 0, y: i * (NODE_H + ROW_GAP) }),
    );
    return out;
  }
  const byDepth = new Map<number, string[]>();
  for (const n of nodes) {
    const d = depth.get(n.node_key) ?? 0;
    const list = byDepth.get(d) ?? [];
    list.push(n.node_key);
    byDepth.set(d, list);
  }
  const depthsAsc = [...byDepth.keys()].sort((a, b) => a - b);
  const maxColSize = Math.max(1, ...[...byDepth.values()].map((c) => c.length));
  const totalH = maxColSize * (NODE_H + ROW_GAP) - ROW_GAP;
  depthsAsc.forEach((d, dIndex) => {
    const col = byDepth.get(d)!;
    const colH = col.length * (NODE_H + ROW_GAP) - ROW_GAP;
    const startY = Math.max(0, (totalH - colH) / 2);
    col.forEach((key, i) => {
      out.set(key, {
        x: dIndex * (NODE_W + COLUMN_GAP),
        y: startY + i * (NODE_H + ROW_GAP),
      });
    });
  });
  return out;
}

// ---------------------------------------------------------------------------
// Node rendering
// ---------------------------------------------------------------------------

// Per-type visual tone. Reuses semantic tokens (primary/secondary/muted)
// already used by status-badges.tsx — no new palette introduced. The
// types are the closed P0 set; an unknown future type falls back to a
// neutral outline tone (server-driven enum default branch rule).
interface NodeTone {
  container: string;
  badge: string;
  Icon: typeof Bot;
}
const NODE_TONES: Record<string, NodeTone> = {
  agent: {
    container: "border-primary/40 bg-primary/5",
    badge: "bg-primary/15 text-primary",
    Icon: Bot,
  },
  acceptance: {
    container: "border-secondary bg-secondary/40",
    badge: "bg-secondary-foreground/10 text-secondary-foreground",
    Icon: ShieldCheck,
  },
  end: {
    container: "border-muted bg-muted/50",
    badge: "bg-muted text-muted-foreground",
    Icon: Flag,
  },
};
const NEUTRAL_TONE: NodeTone = {
  container: "border-border bg-muted/30",
  badge: "bg-muted text-muted-foreground",
  Icon: WorkflowIcon,
};
function toneFor(type: string): NodeTone {
  return NODE_TONES[type] ?? NEUTRAL_TONE;
}

type CanvasNodeData = { templateNode: WorkflowTemplateNode };
type CanvasNodeType = Node<CanvasNodeData, "canvas">;

function CanvasNode({ data, selected }: NodeProps<CanvasNodeType>) {
  const { t } = useT("workflows");
  const node = data.templateNode;
  const tone = toneFor(node.type);
  const Icon = tone.Icon;
  const typeLabels: Record<string, string> = {
    agent: t(($) => $.canvas.node_type_agent),
    acceptance: t(($) => $.canvas.node_type_acceptance),
    end: t(($) => $.canvas.node_type_end),
  };
  const cfg = node.config ?? {};
  const role = cfg.role;
  const roleLabels: Record<string, string> = {
    executor: t(($) => $.detail.role_executor),
    evaluator: t(($) => $.detail.role_evaluator),
    reviewer: t(($) => $.detail.role_reviewer),
  };
  return (
    <div
      className={cn(
        "flex w-[220px] flex-col gap-1 rounded-lg border-2 bg-background p-3 shadow-sm transition-shadow",
        tone.container,
        selected && "ring-2 ring-ring/50",
      )}
    >
      <Handle
        type="target"
        position={Position.Top}
        className="!size-2 !border-none !bg-muted-foreground/50"
      />
      <div className="flex items-center gap-1.5">
        <span
          className={cn(
            "inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-[10px] font-medium",
            tone.badge,
          )}
        >
          <Icon aria-hidden className="size-3" />
          {typeLabels[node.type] ?? node.type}
        </span>
        {node.type === "agent" && role && (
          <span className="text-[10px] text-muted-foreground">
            {roleLabels[role] ?? role}
          </span>
        )}
      </div>
      <div className="truncate text-sm font-medium">
        {node.name || node.node_key}
      </div>
      <div className="truncate font-mono text-[11px] text-muted-foreground">
        {node.node_key}
      </div>
      <Handle
        type="source"
        position={Position.Bottom}
        className="!size-2 !border-none !bg-muted-foreground/50"
      />
    </div>
  );
}

const nodeTypes = { canvas: CanvasNode };

// ---------------------------------------------------------------------------
// Read-only details panel
// ---------------------------------------------------------------------------

function DetailRow({
  label,
  value,
  mono,
}: {
  label: string;
  value: string;
  mono?: boolean;
}) {
  return (
    <div className="flex flex-col gap-0.5">
      <span className="text-xs text-muted-foreground">{label}</span>
      <span className={cn("text-xs", mono && "font-mono")}>{value}</span>
    </div>
  );
}

function NodeDetailsPanel({
  node,
  emptyHint,
  children,
}: {
  node: WorkflowTemplateNode | null;
  emptyHint: string;
  children?: ReactNode;
}) {
  const { t } = useT("workflows");
  if (!node) {
    return (
      <div className="flex h-full flex-col gap-2 border-l border-border p-4 text-xs text-muted-foreground">
        <p>{emptyHint}</p>
      </div>
    );
  }
  const cfg = node.config ?? {};
  const fields = cfg.exit_fields?.fields ?? [];
  const roleLabels: Record<string, string> = {
    executor: t(($) => $.detail.role_executor),
    evaluator: t(($) => $.detail.role_evaluator),
    reviewer: t(($) => $.detail.role_reviewer),
  };
  const typeLabels: Record<string, string> = {
    agent: t(($) => $.canvas.node_type_agent),
    acceptance: t(($) => $.canvas.node_type_acceptance),
    end: t(($) => $.canvas.node_type_end),
  };
  return (
    <div className="flex h-full flex-col gap-3 overflow-auto border-l border-border p-4">
      <div className="flex flex-col gap-1">
        <span className="text-xs uppercase tracking-wide text-muted-foreground">
          {typeLabels[node.type] ?? node.type}
        </span>
        <span className="text-sm font-medium">
          {node.name || node.node_key}
        </span>
        <span className="font-mono text-xs text-muted-foreground">
          {node.node_key}
        </span>
      </div>
      {node.type === "agent" && (
        <>
          <DetailRow
            label={t(($) => $.detail.role_label)}
            value={
              cfg.role ? (roleLabels[cfg.role] ?? cfg.role) : "—"
            }
          />
          <DetailRow
            label={t(($) => $.detail.agent_selector_label)}
            value={cfg.agent_selector ?? cfg.agent_id ?? "—"}
            mono
          />
          {cfg.instructions && cfg.instructions !== "" && (
            <div className="flex flex-col gap-1">
              <span className="text-xs text-muted-foreground">
                {t(($) => $.detail.instructions_label)}
              </span>
              <p className="text-xs">{cfg.instructions}</p>
            </div>
          )}
          {typeof cfg.max_attempts === "number" && cfg.max_attempts > 0 && (
            <DetailRow
              label={t(($) => $.detail.max_attempts_label)}
              value={String(cfg.max_attempts)}
            />
          )}
        </>
      )}
      {node.type === "acceptance" && cfg.auto_pass === true && (
        <DetailRow
          label={t(($) => $.detail.auto_pass_label)}
          value={t(($) => $.canvas.enabled)}
        />
      )}
      {fields.length > 0 && (
        <div className="flex flex-col gap-1">
          <span className="text-xs text-muted-foreground">
            {t(($) => $.detail.exit_fields_title)}
          </span>
          <ul className="flex flex-col gap-1">
            {fields.map((f, i) => (
              <li
                key={`${f.name}-${i}`}
                className="font-mono text-[11px]"
              >
                <span>{f.name}</span>
                {f.required === true && (
                  <span className="text-destructive">*</span>
                )}
                <span className="text-muted-foreground">
                  {`: ${f.type || "any"}`}
                </span>
              </li>
            ))}
          </ul>
        </div>
      )}
      {children}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Public component
// ---------------------------------------------------------------------------

export interface WorkflowCanvasProps {
  template: WorkflowTemplateDetail;
  className?: string;
}

export function WorkflowCanvas({ template, className }: WorkflowCanvasProps) {
  const { t } = useT("workflows");
  const [selected, setSelected] = useState<WorkflowTemplateNode | null>(null);

  // Compute the initial nodes/edges from the server payload. useMemo so
  // the same template object doesn't re-seed local drag state on every
  // parent render (the parent detail page keys its editor on
  // `${id}:${updated_at}`, so a server refetch already forces a remount
  // and this memo recomputes only then).
  const initial = useMemo(() => {
    const positions = computeLayeredLayout(template.nodes, template.edges);
    const built = template.nodes.map<CanvasNodeType>((n) => ({
      id: n.node_key,
      type: "canvas",
      position: positions.get(n.node_key) ?? { x: 0, y: 0 },
      data: { templateNode: n },
    }));
    const builtEdges = template.edges.map<Edge>((e, i) => ({
      id: `e${i}-${e.from_node_key}-${e.to_node_key}`,
      source: e.from_node_key,
      target: e.to_node_key,
      markerEnd: { type: MarkerType.ArrowClosed },
    }));
    return { nodes: built, edges: builtEdges };
  }, [template]);

  const [nodes, setNodes, onNodesChange] = useNodesState<CanvasNodeType>(
    initial.nodes,
  );
  const [edges, , onEdgesChange] = useEdgesState(initial.edges);

  // If the memo recomputes (new payload), reset local state so the user
  // doesn't see stale drag offsets against the new graph.
  useEffect(() => {
    setNodes(initial.nodes);
  }, [initial, setNodes]);

  const onNodeClick: NodeMouseHandler<CanvasNodeType> = useCallback(
    (_event, node) => {
      setSelected(node.data.templateNode);
    },
    [],
  );

  return (
    <div className={cn("flex flex-1 overflow-hidden", className)}>
      <div className="relative h-full min-h-[420px] flex-1">
        <ReactFlow
          nodes={nodes}
          edges={edges}
          nodeTypes={nodeTypes}
          onNodesChange={onNodesChange}
          onEdgesChange={onEdgesChange}
          onNodeClick={onNodeClick}
          nodesDraggable
          edgesFocusable={false}
          proOptions={{ hideAttribution: true }}
          fitView
          className="bg-background"
        >
          <Background
            variant={BackgroundVariant.Dots}
            gap={20}
            size={1}
            className="!text-muted-foreground/40"
          />
          <Controls showInteractive={false} />
        </ReactFlow>
      </div>
      <aside className="hidden w-72 shrink-0 md:block">
        <NodeDetailsPanel
          node={selected}
          emptyHint={t(($) => $.canvas.empty_selection_hint)}
        >
          <div className="mt-auto rounded-md border border-dashed border-border p-2 text-[11px] text-muted-foreground">
            {t(($) => $.canvas.readonly_hint)}
          </div>
        </NodeDetailsPanel>
      </aside>
    </div>
  );
}
