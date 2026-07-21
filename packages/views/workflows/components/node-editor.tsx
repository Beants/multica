"use client";

// node-editor.tsx — the form-based (non-canvas) node editor for a draft
// template. P0 chains are linear, so the editor is an ordered list: each
// node edits type / role / agent selector / instructions / exit-field
// schema inline; edges are derived from list order at save time (i → i+1).

import { ArrowDown, ArrowUp, Plus, Trash2 } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { agentListOptions } from "@multica/core/workspace/queries";
import { useWorkspaceId } from "@multica/core/hooks";
import { Button } from "@multica/ui/components/ui/button";
import { Checkbox } from "@multica/ui/components/ui/checkbox";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { Switch } from "@multica/ui/components/ui/switch";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { useT } from "../../i18n";

export interface EditableExitField {
  /** Client-side row id — a stable React key so removing a middle row
      doesn't shift DOM state onto its neighbours (index keys do). Never
      serialised into the save payload. */
  rowId: string;
  name: string;
  type: string;
  required: boolean;
  description: string;
}

let nextRowId = 1;
export function newExitField(): EditableExitField {
  return {
    rowId: `ef-${nextRowId++}`,
    name: "",
    type: "string",
    required: false,
    description: "",
  };
}

export interface EditableNode {
  node_key: string;
  type: string;
  name: string;
  role: string;
  agent_selector: string;
  instructions: string;
  max_attempts: number;
  auto_pass: boolean;
  exit_fields: EditableExitField[];
}

const EXIT_FIELD_TYPES = ["string", "number", "boolean", "object", "array", "any"];

export function emptyNode(index: number): EditableNode {
  return {
    node_key: `node-${index + 1}`,
    type: "agent",
    name: "",
    role: "executor",
    agent_selector: "",
    instructions: "",
    max_attempts: 0,
    auto_pass: false,
    exit_fields: [],
  };
}

function ExitFieldRow({
  field,
  onChange,
  onRemove,
}: {
  field: EditableExitField;
  onChange: (next: EditableExitField) => void;
  onRemove: () => void;
}) {
  const { t } = useT("workflows");
  const typeLabels: Record<string, string> = {
    string: t(($) => $.detail.field_type_string),
    number: t(($) => $.detail.field_type_number),
    boolean: t(($) => $.detail.field_type_boolean),
    object: t(($) => $.detail.field_type_object),
    array: t(($) => $.detail.field_type_array),
    any: t(($) => $.detail.field_type_any),
  };
  return (
    <div className="flex items-center gap-2">
      <Input
        value={field.name}
        onChange={(e) => onChange({ ...field, name: e.target.value })}
        placeholder={t(($) => $.detail.field_name_placeholder)}
        className="w-36 font-mono text-xs"
      />
      <Select
        items={EXIT_FIELD_TYPES.map((ft) => ({
          value: ft,
          label: typeLabels[ft] ?? ft,
        }))}
        value={field.type}
        onValueChange={(v) => onChange({ ...field, type: v ?? "any" })}
      >
        <SelectTrigger size="sm" aria-label={t(($) => $.detail.field_type_string)}>
          <SelectValue>{typeLabels[field.type] ?? field.type}</SelectValue>
        </SelectTrigger>
        <SelectContent>
          {EXIT_FIELD_TYPES.map((ft) => (
            <SelectItem key={ft} value={ft}>
              {typeLabels[ft] ?? ft}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      <Input
        value={field.description}
        onChange={(e) => onChange({ ...field, description: e.target.value })}
        placeholder={t(($) => $.detail.field_description_placeholder)}
        className="flex-1"
      />
      <label className="flex items-center gap-1 text-xs text-muted-foreground">
        <Checkbox
          checked={field.required}
          onCheckedChange={(checked) =>
            onChange({ ...field, required: checked === true })
          }
        />
        {t(($) => $.detail.field_required)}
      </label>
      <Button
        type="button"
        size="icon-xs"
        variant="ghost"
        aria-label={t(($) => $.detail.remove_field)}
        onClick={onRemove}
      >
        <Trash2 aria-hidden="true" />
      </Button>
    </div>
  );
}

// Agent selector: a dropdown over the workspace's agents (consistent with
// the template-create dialog) that still accepts the current free-form
// value — a selector may be a raw UUID or an agent from another workspace,
// neither of which appears in the list. The extra option round-trips it
// instead of blanking.
function AgentSelectorInput({
  value,
  onChange,
}: {
  value: string;
  onChange: (next: string) => void;
}) {
  const { t } = useT("workflows");
  const wsId = useWorkspaceId();
  const { data: agents = [] } = useQuery(agentListOptions(wsId));
  const known = value === "" || agents.some((a) => a.name === value);
  const items = [
    ...agents.map((a) => ({ value: a.name, label: a.name })),
    ...(!known
      ? [{ value, label: t(($) => $.detail.agent_selector_custom, { name: value }) }]
      : []),
  ];
  return (
    <Select
      items={items}
      value={value}
      onValueChange={(v) => onChange(v ?? "")}
    >
      <SelectTrigger
        size="sm"
        className="w-full"
        aria-label={t(($) => $.detail.agent_selector_label)}
      >
        <SelectValue>
          {value === ""
            ? t(($) => $.detail.agent_selector_placeholder)
            : known
              ? value
              : t(($) => $.detail.agent_selector_custom, { name: value })}
        </SelectValue>
      </SelectTrigger>
      <SelectContent>
        {agents.map((agent) => (
          <SelectItem key={agent.id} value={agent.name}>
            {agent.name}
          </SelectItem>
        ))}
        {!known && (
          <SelectItem value={value}>
            {t(($) => $.detail.agent_selector_custom, { name: value })}
          </SelectItem>
        )}
      </SelectContent>
    </Select>
  );
}

export function NodeEditorCard({
  node,
  index,
  total,
  onChange,
  onMove,
  onRemove,
}: {
  node: EditableNode;
  index: number;
  total: number;
  onChange: (next: EditableNode) => void;
  onMove: (index: number, delta: -1 | 1) => void;
  onRemove: (index: number) => void;
}) {
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
  const isAgent = node.type === "agent";
  const isAcceptance = node.type === "acceptance";

  return (
    <div className="flex flex-col gap-3 rounded-lg border border-border p-3">
      <div className="flex items-center gap-2">
        <Input
          value={node.node_key}
          onChange={(e) => onChange({ ...node, node_key: e.target.value })}
          placeholder={t(($) => $.detail.node_key_placeholder)}
          aria-label={t(($) => $.detail.node_key_label)}
          className="w-40 font-mono text-xs"
        />
        <Input
          value={node.name}
          onChange={(e) => onChange({ ...node, name: e.target.value })}
          placeholder={t(($) => $.detail.node_name_placeholder)}
          aria-label={t(($) => $.detail.node_name_label)}
          className="flex-1"
        />
        <Select
          items={["agent", "acceptance", "end"].map((nt) => ({
            value: nt,
            label: typeLabels[nt] ?? nt,
          }))}
          value={node.type}
          onValueChange={(v) => onChange({ ...node, type: v ?? "agent" })}
        >
          <SelectTrigger size="sm" aria-label={t(($) => $.detail.node_type_label)}>
            <SelectValue>{typeLabels[node.type] ?? node.type}</SelectValue>
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="agent">{typeLabels.agent}</SelectItem>
            <SelectItem value="acceptance">{typeLabels.acceptance}</SelectItem>
            <SelectItem value="end">{typeLabels.end}</SelectItem>
          </SelectContent>
        </Select>
        <Button
          type="button"
          size="icon-xs"
          variant="ghost"
          aria-label={t(($) => $.detail.move_up)}
          disabled={index === 0}
          onClick={() => onMove(index, -1)}
        >
          <ArrowUp aria-hidden="true" />
        </Button>
        <Button
          type="button"
          size="icon-xs"
          variant="ghost"
          aria-label={t(($) => $.detail.move_down)}
          disabled={index === total - 1}
          onClick={() => onMove(index, 1)}
        >
          <ArrowDown aria-hidden="true" />
        </Button>
        <Button
          type="button"
          size="icon-xs"
          variant="ghost"
          aria-label={t(($) => $.detail.remove_node)}
          onClick={() => onRemove(index)}
        >
          <Trash2 aria-hidden="true" />
        </Button>
      </div>

      {isAgent && (
        <>
          <div className="flex items-center gap-2">
            <div className="flex flex-col gap-1.5">
              <Label className="text-xs">{t(($) => $.detail.role_label)}</Label>
              <Select
                items={["executor", "evaluator", "reviewer"].map((r) => ({
                  value: r,
                  label: roleLabels[r] ?? r,
                }))}
                value={node.role}
                onValueChange={(v) => onChange({ ...node, role: v ?? "executor" })}
              >
                <SelectTrigger size="sm" aria-label={t(($) => $.detail.role_label)}>
                  <SelectValue>{roleLabels[node.role] ?? node.role}</SelectValue>
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="executor">{roleLabels.executor}</SelectItem>
                  <SelectItem value="evaluator">{roleLabels.evaluator}</SelectItem>
                  <SelectItem value="reviewer">{roleLabels.reviewer}</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="flex flex-1 flex-col gap-1.5">
              <Label className="text-xs">{t(($) => $.detail.agent_selector_label)}</Label>
              <AgentSelectorInput
                value={node.agent_selector}
                onChange={(next) => onChange({ ...node, agent_selector: next })}
              />
            </div>
            <div className="flex flex-col gap-1.5">
              <Label className="text-xs">{t(($) => $.detail.max_attempts_label)}</Label>
              <Input
                type="number"
                min={0}
                value={node.max_attempts === 0 ? "" : String(node.max_attempts)}
                onChange={(e) => {
                  const n = Number.parseInt(e.target.value, 10);
                  onChange({ ...node, max_attempts: Number.isNaN(n) ? 0 : n });
                }}
                className="w-20"
              />
            </div>
          </div>
          <div className="flex flex-col gap-1.5">
            <Label className="text-xs">{t(($) => $.detail.instructions_label)}</Label>
            <Textarea
              value={node.instructions}
              onChange={(e) => onChange({ ...node, instructions: e.target.value })}
              placeholder={t(($) => $.detail.instructions_placeholder)}
              rows={3}
            />
          </div>
        </>
      )}

      {isAcceptance && (
        <label className="flex items-center gap-2 text-xs text-muted-foreground">
          <Switch
            checked={node.auto_pass}
            onCheckedChange={(checked) =>
              onChange({ ...node, auto_pass: checked === true })
            }
          />
          {t(($) => $.detail.auto_pass_label)}
        </label>
      )}

      {(isAgent || isAcceptance) && (
        <div className="flex flex-col gap-2">
          <div className="flex items-center justify-between">
            <Label className="text-xs">{t(($) => $.detail.exit_fields_title)}</Label>
            <Button
              type="button"
              size="xs"
              variant="ghost"
              onClick={() =>
                onChange({
                  ...node,
                  exit_fields: [...node.exit_fields, newExitField()],
                })
              }
            >
              <Plus aria-hidden="true" />
              {t(($) => $.detail.add_field)}
            </Button>
          </div>
          {node.exit_fields.map((field, fi) => (
            <ExitFieldRow
              key={field.rowId}
              field={field}
              onChange={(next) =>
                onChange({
                  ...node,
                  exit_fields: node.exit_fields.map((f, i) => (i === fi ? next : f)),
                })
              }
              onRemove={() =>
                onChange({
                  ...node,
                  exit_fields: node.exit_fields.filter((_, i) => i !== fi),
                })
              }
            />
          ))}
        </div>
      )}
    </div>
  );
}
