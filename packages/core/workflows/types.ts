// types.ts — workflow engine (P0) wire types, mirroring the Go DTOs in
// server/internal/handler/workflow_{template,run,hook}.go. Status fields are
// closed TS unions of the P0 server values; the zod schemas in ./schemas.ts
// keep them as z.string() so an unknown future value parses and renders
// through a default branch instead of white-screening (CLAUDE.md "API
// Response Compatibility"). JSONB payloads (config, exit_fields, gaps,
// artifacts, evidence, snapshot) tolerate unknown shapes per protocol rule
// D-9 — they are typed as unknown/record and pass through untouched.

// ---------------------------------------------------------------------------
// Enums (server-driven; render unknowns via default branches)
// ---------------------------------------------------------------------------

export type WorkflowTemplateStatus = "draft" | "published" | "archived";

export type WorkflowNodeType = "agent" | "acceptance" | "end";

export type WorkflowNodeRole = "executor" | "evaluator" | "reviewer";

export type WorkflowRunStatus =
  | "running"
  | "paused"
  | "waiting_acceptance"
  | "completed"
  | "failed"
  | "cancelled";

export type WorkflowStepStatus =
  | "pending"
  | "active"
  | "dispatched"
  | "running"
  | "passed"
  | "failed"
  | "blocked"
  | "rework"
  | "skipped";

export type WorkflowSubmissionStatus =
  | "DONE"
  | "DONE_WITH_CONCERNS"
  | "BLOCKED"
  | "NEEDS_CONTEXT";

export type WorkflowVerdictResult = "pass" | "fail" | "blocked";

export type WorkflowAcceptanceStatus = "pending" | "approved" | "rejected";

export type WorkflowHookStatus = "active" | "disabled";

// ---------------------------------------------------------------------------
// Templates
// ---------------------------------------------------------------------------

// One declared exit field on a node (准出 schema). Type is a JSON type name;
// empty/"any" means unconstrained. Mirrors workflow.ExitFieldSpec.
export interface WorkflowExitFieldSpec {
  name: string;
  type: string;
  required?: boolean;
  description?: string;
}

// Node config JSONB. Mirrors workflow.NodeConfig — unknown keys tolerated
// (forward-compat D-9), so this interface lists only the P0-known fields.
export interface WorkflowNodeConfig {
  role?: WorkflowNodeRole;
  agent_selector?: string;
  agent_id?: string;
  instructions?: string;
  exit_fields?: { fields?: WorkflowExitFieldSpec[] };
  max_attempts?: number;
  auto_pass?: boolean;
  reviewer_id?: string;
}

export interface WorkflowTemplate {
  id: string;
  key: string;
  name: string;
  description: string;
  version: number;
  status: WorkflowTemplateStatus;
  created_at: string;
  updated_at: string;
}

export interface WorkflowTemplateNode {
  id: string;
  node_key: string;
  type: WorkflowNodeType;
  name: string;
  config: WorkflowNodeConfig;
  position?: Record<string, unknown>;
}

export interface WorkflowTemplateEdge {
  id: string;
  from_node_key: string;
  to_node_key: string;
  priority: number;
  // P1-2 conditional routing: JSONLogic expression (or undefined for a
  // catch-all edge). The frontend treats this as an opaque JSON blob — the
  // engine evaluates it; the UI only carries/transports it (MVP: API-only
  // configuration; visual builder lands in P3).
  condition?: unknown;
}

export interface WorkflowTemplateDetail extends WorkflowTemplate {
  nodes: WorkflowTemplateNode[];
  edges: WorkflowTemplateEdge[];
}

// Node/edge input for create/update. Edges reference node_keys (row UUIDs
// are an internal detail). P0 is linear: the UI derives edges from node
// order, but the request shape carries them explicitly.
export interface WorkflowNodeInput {
  node_key: string;
  type: string;
  name: string;
  config?: WorkflowNodeConfig;
}

export interface WorkflowEdgeInput {
  from_node_key: string;
  to_node_key: string;
  priority?: number;
  // P1-2 conditional routing: JSONLogic expression. Omitted/undefined =
  // catch-all. The HTTP transport (workflowEdgeInput in
  // server/internal/handler/workflow_template.go) does not yet parse this
  // field; P3 will wire it through. Programmatic clients publishing via the
  // service layer can already set it.
  condition?: unknown;
}

export interface CreateWorkflowTemplateRequest {
  key: string;
  name: string;
  description?: string;
  nodes: WorkflowNodeInput[];
  edges: WorkflowEdgeInput[];
}

export interface UpdateWorkflowTemplateRequest {
  name?: string;
  description?: string;
  // nodes+edges must be supplied together (server 400s on exactly one);
  // together they replace the whole graph.
  nodes?: WorkflowNodeInput[];
  edges?: WorkflowEdgeInput[];
}

// ---------------------------------------------------------------------------
// Hooks (management; the cleartext token is returned exactly once at create)
// ---------------------------------------------------------------------------

export interface WorkflowHook {
  id: string;
  template_id: string;
  name: string;
  status: WorkflowHookStatus;
  last_used_at?: string | null;
  created_at: string;
}

export interface CreateWorkflowHookRequest {
  template_id: string;
  name: string;
}

export interface CreateWorkflowHookResponse extends WorkflowHook {
  token: string;
}

// P1-fe-2: Rules asset (P1-4 API). Three-level team constraints bound to
// node/template/agent/project via rule_binding.
export type WorkflowRuleLevel = "hard" | "soft" | "safety";
export type WorkflowRuleScope = "workspace" | "project" | "agent";

export interface WorkflowRule {
  id: string;
  workspace_id: string;
  name: string;
  level: WorkflowRuleLevel;
  scope: WorkflowRuleScope;
  content: string;
  config?: unknown;
  status: string;
  version: number;
  created_at: string;
  updated_at: string;
}

export type WorkflowRuleBindingTarget = "node" | "template" | "agent" | "project";
export type WorkflowRuleEnforcement = "gate_check" | "context_inject";

export interface WorkflowRuleBinding {
  id: string;
  rule_id: string;
  target_type: WorkflowRuleBindingTarget;
  target_id: string;
  enforcement: WorkflowRuleEnforcement;
  created_at: string;
}

export interface CreateWorkflowRuleRequest {
  name: string;
  level: WorkflowRuleLevel;
  scope?: WorkflowRuleScope;
  content: string;
  config?: unknown;
  status?: string;
}

export interface CreateWorkflowRuleBindingRequest {
  target_type: WorkflowRuleBindingTarget;
  target_id: string;
  enforcement?: WorkflowRuleEnforcement;
}

// P1-fe-3: agent capability labels (P1-7 dispatch matcher data). Managed via
// /api/agents/{id}/capabilities; consumed by MatchAgentByCapability at dispatch.
export interface AgentCapability {
  id: string;
  agent_id: string;
  capability_key: string;
  proficiency: number;
  evidence?: unknown;
  updated_at: string;
  created_at: string;
}

export interface CreateAgentCapabilityRequest {
  capability_key: string;
  proficiency: number;
}

// ---------------------------------------------------------------------------
// Runs (AC4 trace surface)
// ---------------------------------------------------------------------------

export interface WorkflowRun {
  id: string;
  template_id: string;
  status: WorkflowRunStatus;
  source_type: string;
  source_id?: string | null;
  intake_issue_id?: string | null;
  started_at: string;
  completed_at?: string | null;
  updated_at: string;
}

export interface WorkflowStep {
  id: string;
  node_key: string;
  status: WorkflowStepStatus;
  attempt: number;
  agent_id?: string | null;
  agent_task_id?: string | null;
  issue_id?: string | null;
  exit_fields?: Record<string, unknown>;
  started_at?: string | null;
  finished_at?: string | null;
  created_at: string;
}

export interface WorkflowSubmission {
  id: string;
  step_instance_id: string;
  status: WorkflowSubmissionStatus;
  gaps?: unknown;
  artifacts?: unknown;
  exit_fields?: Record<string, unknown>;
  raw_summary?: string | null;
  created_at: string;
}

export interface WorkflowVerdict {
  id: string;
  submission_id: string;
  step_instance_id: string;
  result: WorkflowVerdictResult;
  root_cause?: string | null;
  confidence?: number | null;
  evidence?: unknown;
  verdict_by: string;
  created_at: string;
}

export interface WorkflowAcceptance {
  id: string;
  step_instance_id: string;
  status: WorkflowAcceptanceStatus;
  reviewer_id?: string | null;
  decided_at?: string | null;
  reject_reason?: string | null;
  reject_to_node_key?: string | null;
  rework_context?: unknown;
  created_at: string;
}

export interface WorkflowTransition {
  id: string;
  step_instance_id: string;
  from_status: string;
  to_status: string;
  attempt: number;
  trigger_by: string;
  payload?: unknown;
  created_at: string;
}

// Frozen template graph carried by every run (workflow.Snapshot).
export interface WorkflowTemplateSnapshot {
  template_id: string;
  key: string;
  version: number;
  nodes: {
    node_key: string;
    type: string;
    name: string;
    config: WorkflowNodeConfig;
  }[];
  edges: {
    from_node_key: string;
    to_node_key: string;
    priority: number;
    // P1-2: present when this edge carries a JSONLogic condition; absent
    // for catch-all edges. Forwarded untouched for display/debugging —
    // the engine has already evaluated it server-side.
    condition?: unknown;
  }[];
}

export interface WorkflowRunDetail extends WorkflowRun {
  template_snapshot: WorkflowTemplateSnapshot;
  steps: WorkflowStep[];
  submissions: WorkflowSubmission[];
  verdicts: WorkflowVerdict[];
  acceptances: WorkflowAcceptance[];
  transitions: WorkflowTransition[];
}

// P1-fe-1: one step's seven-element diagnosis (军团文 §4.5). Mirrors the
// server stepDiagnosisDTO in workflow_run_diagnosis.go.
export interface StepDiagnosis {
  step_id: string;
  node_key: string;
  run_id: string;
  task_id?: string; // ①
  agent_id?: string; // ②
  attempt: number; // ⑥
  max_attempts?: number; // ⑥
  final_status: string; // ⑦
  ok: boolean;
  failure_type?: string; // ③ fail/blocked/rework/gate_reject
  reason?: string; // ④ assembled failure reason
  output?: unknown; // ⑤ gate_run.output / agent_task.result
  transitions: WorkflowTransition[]; // ⑥ retry history
}

export interface RunDiagnosis {
  run_id: string;
  run_status: string;
  steps: StepDiagnosis[];
}

export interface RejectAcceptanceRequest {
  reject_to_node_key: string;
  reason: string;
}

export interface AcceptanceDecisionResponse {
  run_id: string;
  acceptance_id: string;
  status: string;
}
