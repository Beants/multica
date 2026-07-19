// schemas.ts — zod boundary schemas for the workflow engine API (P0 fork
// file). Same leniency contract as api/schemas.ts (see the comment block
// there): string enums stay z.string() so a new server-side status renders
// as a generic fallback instead of failing safeParse; arrays default to [];
// every object is .loose() so unknown server fields pass through (protocol
// rule D-9). Every client method in api/client.ts parses through these with
// parseWithFallback + an EMPTY_* fallback.

import { z } from "zod";
import type {
  AcceptanceDecisionResponse,
  CreateWorkflowHookResponse,
  WorkflowHook,
  WorkflowRun,
  WorkflowRunDetail,
  WorkflowTemplate,
  WorkflowTemplateDetail,
} from "./types";

// A JSONB blob that must be an object when present (config, exit_fields,
// position). Tolerates null (Go's json.RawMessage marshals an empty column
// as null) and missing.
const jsonObject = z.record(z.string(), z.unknown()).nullish();

// A JSONB blob of any shape (gaps, artifacts, evidence, payload,
// rework_context) — pass through untouched (D-9).
const jsonAny = z.unknown().optional();

// ---------------------------------------------------------------------------
// Templates
// ---------------------------------------------------------------------------

export const WorkflowNodeConfigSchema = z
  .object({
    role: z.string().optional(),
    agent_selector: z.string().optional(),
    agent_id: z.string().optional(),
    instructions: z.string().optional(),
    exit_fields: z
      .object({
        fields: z
          .array(
            z
              .object({
                name: z.string(),
                type: z.string().optional().default(""),
                required: z.boolean().optional(),
                description: z.string().optional(),
              })
              .loose(),
          )
          .optional(),
      })
      .loose()
      .optional(),
    max_attempts: z.number().optional(),
    auto_pass: z.boolean().optional(),
    reviewer_id: z.string().optional(),
  })
  .loose();

export const WorkflowTemplateSchema = z
  .object({
    id: z.string(),
    key: z.string(),
    name: z.string(),
    description: z.string().optional().default(""),
    version: z.number().optional().default(1),
    status: z.string().optional().default("draft"),
    created_at: z.string().optional().default(""),
    updated_at: z.string().optional().default(""),
  })
  .loose();

export const WorkflowTemplateListSchema = z.array(WorkflowTemplateSchema).default([]);

export const WorkflowTemplateNodeSchema = z
  .object({
    id: z.string().optional().default(""),
    node_key: z.string(),
    type: z.string().optional().default("agent"),
    name: z.string().optional().default(""),
    config: WorkflowNodeConfigSchema.nullish(),
    position: jsonObject,
  })
  .loose();

export const WorkflowTemplateEdgeSchema = z
  .object({
    id: z.string().optional().default(""),
    from_node_key: z.string(),
    to_node_key: z.string(),
    priority: z.number().optional().default(0),
  })
  .loose();

export const WorkflowTemplateDetailSchema = WorkflowTemplateSchema.extend({
  nodes: z.array(WorkflowTemplateNodeSchema).default([]),
  edges: z.array(WorkflowTemplateEdgeSchema).default([]),
}).loose();

export const EMPTY_WORKFLOW_TEMPLATE: WorkflowTemplate = {
  id: "",
  key: "",
  name: "",
  description: "",
  version: 1,
  status: "draft",
  created_at: "",
  updated_at: "",
};

export const EMPTY_WORKFLOW_TEMPLATE_LIST: WorkflowTemplate[] = [];

export const EMPTY_WORKFLOW_TEMPLATE_DETAIL: WorkflowTemplateDetail = {
  ...EMPTY_WORKFLOW_TEMPLATE,
  nodes: [],
  edges: [],
};

// ---------------------------------------------------------------------------
// Hooks
// ---------------------------------------------------------------------------

export const WorkflowHookSchema = z
  .object({
    id: z.string(),
    template_id: z.string(),
    name: z.string(),
    status: z.string().optional().default("active"),
    last_used_at: z.string().nullish(),
    created_at: z.string().optional().default(""),
  })
  .loose();

export const WorkflowHookListSchema = z.array(WorkflowHookSchema).default([]);

export const CreateWorkflowHookResponseSchema = WorkflowHookSchema.extend({
  token: z.string().optional().default(""),
}).loose();

export const EMPTY_WORKFLOW_HOOK_LIST: WorkflowHook[] = [];

export const EMPTY_WORKFLOW_HOOK: WorkflowHook = {
  id: "",
  template_id: "",
  name: "",
  status: "active",
  last_used_at: null,
  created_at: "",
};

export const EMPTY_CREATE_WORKFLOW_HOOK_RESPONSE: CreateWorkflowHookResponse = {
  id: "",
  template_id: "",
  name: "",
  status: "active",
  last_used_at: null,
  created_at: "",
  token: "",
};

// ---------------------------------------------------------------------------
// Runs
// ---------------------------------------------------------------------------

export const WorkflowRunSchema = z
  .object({
    id: z.string(),
    template_id: z.string(),
    status: z.string().optional().default("running"),
    source_type: z.string().optional().default(""),
    source_id: z.string().nullish(),
    intake_issue_id: z.string().nullish(),
    started_at: z.string().optional().default(""),
    completed_at: z.string().nullish(),
    updated_at: z.string().optional().default(""),
  })
  .loose();

export const WorkflowRunListSchema = z.array(WorkflowRunSchema).default([]);

export const WorkflowStepSchema = z
  .object({
    id: z.string(),
    node_key: z.string(),
    status: z.string().optional().default("pending"),
    attempt: z.number().optional().default(1),
    agent_id: z.string().nullish(),
    agent_task_id: z.string().nullish(),
    issue_id: z.string().nullish(),
    exit_fields: jsonObject,
    started_at: z.string().nullish(),
    finished_at: z.string().nullish(),
    created_at: z.string().optional().default(""),
  })
  .loose();

export const WorkflowSubmissionSchema = z
  .object({
    id: z.string(),
    step_instance_id: z.string(),
    status: z.string().optional().default("DONE"),
    gaps: jsonAny,
    artifacts: jsonAny,
    exit_fields: jsonObject,
    raw_summary: z.string().nullish(),
    created_at: z.string().optional().default(""),
  })
  .loose();

export const WorkflowVerdictSchema = z
  .object({
    id: z.string(),
    submission_id: z.string().optional().default(""),
    step_instance_id: z.string(),
    result: z.string().optional().default("pass"),
    root_cause: z.string().nullish(),
    confidence: z.number().nullish(),
    evidence: jsonAny,
    verdict_by: z.string().optional().default("system"),
    created_at: z.string().optional().default(""),
  })
  .loose();

export const WorkflowAcceptanceSchema = z
  .object({
    id: z.string(),
    step_instance_id: z.string(),
    status: z.string().optional().default("pending"),
    reviewer_id: z.string().nullish(),
    decided_at: z.string().nullish(),
    reject_reason: z.string().nullish(),
    reject_to_node_key: z.string().nullish(),
    rework_context: jsonAny,
    created_at: z.string().optional().default(""),
  })
  .loose();

export const WorkflowTransitionSchema = z
  .object({
    id: z.string(),
    step_instance_id: z.string(),
    from_status: z.string().optional().default(""),
    to_status: z.string(),
    attempt: z.number().optional().default(1),
    trigger_by: z.string().optional().default(""),
    payload: jsonAny,
    created_at: z.string().optional().default(""),
  })
  .loose();

export const WorkflowTemplateSnapshotSchema = z
  .object({
    template_id: z.string().optional().default(""),
    key: z.string().optional().default(""),
    version: z.number().optional().default(1),
    nodes: z
      .array(
        z
          .object({
            node_key: z.string(),
            type: z.string().optional().default("agent"),
            name: z.string().optional().default(""),
            config: WorkflowNodeConfigSchema.nullish(),
          })
          .loose(),
      )
      .default([]),
    edges: z
      .array(
        z
          .object({
            from_node_key: z.string(),
            to_node_key: z.string(),
            priority: z.number().optional().default(0),
          })
          .loose(),
      )
      .default([]),
  })
  .loose();

export const WorkflowRunDetailSchema = WorkflowRunSchema.extend({
  template_snapshot: WorkflowTemplateSnapshotSchema.nullish(),
  steps: z.array(WorkflowStepSchema).default([]),
  submissions: z.array(WorkflowSubmissionSchema).default([]),
  verdicts: z.array(WorkflowVerdictSchema).default([]),
  acceptances: z.array(WorkflowAcceptanceSchema).default([]),
  transitions: z.array(WorkflowTransitionSchema).default([]),
}).loose();

export const EMPTY_WORKFLOW_RUN_LIST: WorkflowRun[] = [];

export const EMPTY_WORKFLOW_RUN_DETAIL: WorkflowRunDetail = {
  id: "",
  template_id: "",
  status: "running",
  source_type: "",
  source_id: null,
  intake_issue_id: null,
  started_at: "",
  completed_at: null,
  updated_at: "",
  template_snapshot: { template_id: "", key: "", version: 1, nodes: [], edges: [] },
  steps: [],
  submissions: [],
  verdicts: [],
  acceptances: [],
  transitions: [],
};

export const AcceptanceDecisionSchema = z
  .object({
    run_id: z.string(),
    acceptance_id: z.string(),
    status: z.string(),
  })
  .loose();

export const EMPTY_ACCEPTANCE_DECISION: AcceptanceDecisionResponse = {
  run_id: "",
  acceptance_id: "",
  status: "",
};
