// schemas.test.ts — malformed-response coverage for the workflow engine's
// zod boundary (CLAUDE.md "API Response Compatibility": every new schema
// ships with proof that drift degrades instead of throwing). The contract:
//   - missing optional fields → defaults (arrays [], enums "")
//   - unknown extra fields → pass through (.loose), never rejected
//   - structurally wrong types (array where object expected, etc.) →
//     safeParse fails → parseWithFallback returns the EMPTY_* fallback
//   - null JSONB columns (Go json.RawMessage null) → tolerated

import { describe, expect, it } from "vitest";
import { parseWithFallback } from "../api/schema";
import {
  AcceptanceDecisionSchema,
  CreateWorkflowHookResponseSchema,
  EMPTY_ACCEPTANCE_DECISION,
  EMPTY_CREATE_WORKFLOW_HOOK_RESPONSE,
  EMPTY_WORKFLOW_HOOK_LIST,
  EMPTY_WORKFLOW_RUN_DETAIL,
  EMPTY_WORKFLOW_RUN_LIST,
  EMPTY_WORKFLOW_TEMPLATE_DETAIL,
  EMPTY_WORKFLOW_TEMPLATE_LIST,
  WorkflowHookListSchema,
  WorkflowRunDetailSchema,
  WorkflowRunListSchema,
  WorkflowTemplateDetailSchema,
  WorkflowTemplateListSchema,
} from "./schemas";

describe("WorkflowTemplateListSchema", () => {
  it("parses a well-formed list", () => {
    const parsed = WorkflowTemplateListSchema.parse([
      { id: "t1", key: "standard", name: "Standard", version: 2, status: "published" },
    ]);
    expect(parsed).toHaveLength(1);
    expect(parsed[0]).toMatchObject({ key: "standard", version: 2, description: "" });
  });

  it("tolerates unknown fields and unknown status values (forward-compat)", () => {
    const parsed = WorkflowTemplateListSchema.parse([
      {
        id: "t1",
        key: "standard",
        name: "Standard",
        status: "future-status",
        future_knob: { nested: true },
      },
    ]);
    expect(parsed[0]).toMatchObject({ status: "future-status", future_knob: { nested: true } });
  });

  it("falls back to [] for a non-array payload", () => {
    const out = parseWithFallback(
      { templates: [] },
      WorkflowTemplateListSchema,
      EMPTY_WORKFLOW_TEMPLATE_LIST,
      { endpoint: "GET /api/workflow-templates" },
    );
    expect(out).toEqual([]);
  });

  it("falls back to [] when an item is structurally broken", () => {
    const out = parseWithFallback(
      [{ id: 42, key: null }],
      WorkflowTemplateListSchema,
      EMPTY_WORKFLOW_TEMPLATE_LIST,
      { endpoint: "GET /api/workflow-templates" },
    );
    expect(out).toEqual([]);
  });
});

describe("WorkflowTemplateDetailSchema", () => {
  it("defaults missing nodes/edges to []", () => {
    const parsed = WorkflowTemplateDetailSchema.parse({ id: "t1", key: "k", name: "n" });
    expect(parsed.nodes).toEqual([]);
    expect(parsed.edges).toEqual([]);
  });

  it("tolerates null node config (Go json.RawMessage null)", () => {
    const parsed = WorkflowTemplateDetailSchema.parse({
      id: "t1",
      key: "k",
      name: "n",
      nodes: [{ node_key: "plan", type: "agent", config: null }],
      edges: [],
    });
    expect(parsed.nodes[0]?.config).toBeNull();
  });

  it("parses node exit_fields schema through to the editor", () => {
    const parsed = WorkflowTemplateDetailSchema.parse({
      id: "t1",
      key: "k",
      name: "n",
      nodes: [
        {
          node_key: "impl",
          type: "agent",
          name: "Implement",
          config: {
            role: "executor",
            instructions: "do it",
            exit_fields: { fields: [{ name: "pr_url", type: "string", required: true }] },
            unknown_future_field: [1, 2],
          },
        },
      ],
      edges: [{ from_node_key: "plan", to_node_key: "impl" }],
    });
    const cfg = parsed.nodes[0]?.config;
    expect(cfg?.exit_fields?.fields?.[0]).toMatchObject({ name: "pr_url", required: true });
    expect(cfg).toMatchObject({ unknown_future_field: [1, 2] });
  });

  it("falls back to the empty detail for a structurally broken payload", () => {
    const out = parseWithFallback(
      "not-an-object",
      WorkflowTemplateDetailSchema,
      EMPTY_WORKFLOW_TEMPLATE_DETAIL,
      { endpoint: "GET /api/workflow-templates/:id" },
    );
    expect(out).toMatchObject({ id: "", nodes: [], edges: [] });
  });
});

describe("WorkflowRunListSchema", () => {
  it("parses runs with optional fields absent", () => {
    const parsed = WorkflowRunListSchema.parse([
      { id: "r1", template_id: "t1", status: "waiting_acceptance" },
    ]);
    expect(parsed[0]).toMatchObject({ id: "r1", status: "waiting_acceptance" });
    // .nullable().optional() convention: absent optionals stay undefined.
    expect(parsed[0]?.source_id).toBeUndefined();
    expect(parsed[0]?.completed_at).toBeUndefined();
  });

  it("falls back to [] for a malformed payload", () => {
    const out = parseWithFallback(null, WorkflowRunListSchema, EMPTY_WORKFLOW_RUN_LIST, {
      endpoint: "GET /api/workflow-runs",
    });
    expect(out).toEqual([]);
  });
});

describe("WorkflowRunDetailSchema", () => {
  const wellFormed = {
    id: "r1",
    template_id: "t1",
    status: "running",
    template_snapshot: {
      template_id: "t1",
      key: "standard",
      version: 3,
      nodes: [{ node_key: "plan", type: "agent", name: "Plan", config: { role: "executor" } }],
      edges: [{ from_node_key: "plan", to_node_key: "impl" }],
    },
    steps: [
      {
        id: "s1",
        node_key: "plan",
        status: "passed",
        attempt: 1,
        exit_fields: { summary: "done" },
      },
    ],
    submissions: [
      {
        id: "sub1",
        step_instance_id: "s1",
        status: "DONE_WITH_CONCERNS",
        gaps: [{ field: "tests" }],
        artifacts: [{ kind: "pr", url: "https://example/pr/1" }],
        exit_fields: null,
        raw_summary: null,
      },
    ],
    verdicts: [
      {
        id: "v1",
        submission_id: "sub1",
        step_instance_id: "s1",
        result: "pass",
        verdict_by: "system",
        evidence: { concerns: ["tests"] },
      },
    ],
    acceptances: [
      { id: "a1", step_instance_id: "s9", status: "pending", reviewer_id: null },
    ],
    transitions: [
      {
        id: "tr1",
        step_instance_id: "s1",
        from_status: "active",
        to_status: "passed",
        attempt: 1,
        trigger_by: "system",
      },
    ],
  };

  it("parses the full AC4 trace payload", () => {
    const parsed = WorkflowRunDetailSchema.parse(wellFormed);
    expect(parsed.steps[0]).toMatchObject({ node_key: "plan", status: "passed" });
    expect(parsed.submissions[0]?.gaps).toEqual([{ field: "tests" }]);
    expect(parsed.verdicts[0]).toMatchObject({ result: "pass", verdict_by: "system" });
    expect(parsed.acceptances[0]?.status).toBe("pending");
    expect(parsed.transitions[0]).toMatchObject({ to_status: "passed", attempt: 1 });
    expect(parsed.template_snapshot?.key).toBe("standard");
  });

  it("defaults every collection when the server omits them", () => {
    const parsed = WorkflowRunDetailSchema.parse({ id: "r1", template_id: "t1" });
    expect(parsed.steps).toEqual([]);
    expect(parsed.submissions).toEqual([]);
    expect(parsed.verdicts).toEqual([]);
    expect(parsed.acceptances).toEqual([]);
    expect(parsed.transitions).toEqual([]);
  });

  it("falls back to the empty detail for a structurally broken payload", () => {
    const out = parseWithFallback(
      { id: 7, steps: "nope" },
      WorkflowRunDetailSchema,
      EMPTY_WORKFLOW_RUN_DETAIL,
      { endpoint: "GET /api/workflow-runs/:id" },
    );
    expect(out).toMatchObject({ id: "", steps: [] });
  });
});

describe("WorkflowHookListSchema", () => {
  it("parses hooks (token hash is never present in list payloads)", () => {
    const parsed = WorkflowHookListSchema.parse([
      { id: "h1", template_id: "t1", name: "linear", status: "active", last_used_at: null },
    ]);
    expect(parsed[0]).toMatchObject({ id: "h1", last_used_at: null });
  });

  it("falls back to [] for a malformed payload", () => {
    const out = parseWithFallback({}, WorkflowHookListSchema, EMPTY_WORKFLOW_HOOK_LIST, {
      endpoint: "GET /api/workflow-hooks",
    });
    expect(out).toEqual([]);
  });

  it("parses the create response carrying the one-time token", () => {
    const parsed = CreateWorkflowHookResponseSchema.parse({
      id: "h1",
      template_id: "t1",
      name: "linear",
      token: "wfh_secret",
    });
    expect(parsed.token).toBe("wfh_secret");
  });

  it("falls back when the create response is structurally broken", () => {
    const out = parseWithFallback(
      [],
      CreateWorkflowHookResponseSchema,
      EMPTY_CREATE_WORKFLOW_HOOK_RESPONSE,
      { endpoint: "POST /api/workflow-hooks" },
    );
    expect(out.token).toBe("");
  });
});

describe("AcceptanceDecisionSchema", () => {
  it("parses approve/reject decisions", () => {
    const parsed = AcceptanceDecisionSchema.parse({
      run_id: "r1",
      acceptance_id: "a1",
      status: "approved",
    });
    expect(parsed.status).toBe("approved");
  });

  it("falls back for a malformed decision payload", () => {
    const out = parseWithFallback(
      { run_id: 1 },
      AcceptanceDecisionSchema,
      EMPTY_ACCEPTANCE_DECISION,
      { endpoint: "POST /api/workflow-runs/:id/acceptance/approve" },
    );
    expect(out).toEqual({ run_id: "", acceptance_id: "", status: "" });
  });
});
