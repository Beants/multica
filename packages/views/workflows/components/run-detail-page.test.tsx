// run-detail-page.test.tsx — the AC4 trace render: step cards in snapshot
// order with submission + verdict inline, the step_transition timeline, and
// the pending-acceptance highlight.

import type { ReactNode } from "react";
import { describe, expect, it, beforeEach, vi } from "vitest";
import { render as rtlRender, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import { RESOURCES } from "../../locales";

const apiMock = vi.hoisted(() => ({
  getWorkflowRun: vi.fn(),
  approveWorkflowAcceptance: vi.fn(),
  rejectWorkflowAcceptance: vi.fn(),
}));

vi.mock("@multica/core/api", () => ({ api: apiMock }));

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({
    workflowRuns: () => "/acme/workflows/runs",
  }),
}));

vi.mock("@multica/core/workflows/flag", () => ({
  useWorkflowEngineFlag: () => true,
}));

// WS subscriptions are a no-op in tests (no WSProvider).
vi.mock("@multica/core/workflows/realtime", () => ({
  useWorkflowRunsRealtime: () => {},
  useWorkflowRunRealtime: () => {},
}));

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

vi.mock("../../navigation", () => ({
  AppLink: ({ children, href }: { children: ReactNode; href: string }) => (
    <a href={href}>{children}</a>
  ),
  useNavigation: () => ({ push: vi.fn(), replace: vi.fn() }),
}));

import { RunDetailPage } from "./run-detail-page";

const RUN_DETAIL = {
  id: "run-00000000-0000-0000-0000-000000000001",
  template_id: "tpl-1",
  status: "waiting_acceptance",
  source_type: "hook",
  source_id: "REQ-42",
  intake_issue_id: "issue-1",
  started_at: "2026-07-17T09:00:00Z",
  updated_at: "2026-07-17T10:00:00Z",
  template_snapshot: {
    template_id: "tpl-1",
    key: "standard",
    version: 2,
    nodes: [
      { node_key: "implement", type: "agent", name: "Implementation", config: { role: "executor" } },
      { node_key: "acceptance", type: "acceptance", name: "Acceptance", config: {} },
      { node_key: "end", type: "end", name: "End", config: {} },
    ],
    edges: [
      { from_node_key: "implement", to_node_key: "acceptance", priority: 0 },
      { from_node_key: "acceptance", to_node_key: "end", priority: 0 },
    ],
  },
  steps: [
    {
      id: "step-1",
      node_key: "implement",
      status: "passed",
      attempt: 1,
      exit_fields: { pr_url: "https://example/pr/1" },
      created_at: "2026-07-17T09:01:00Z",
    },
    {
      id: "step-2",
      node_key: "acceptance",
      status: "active",
      attempt: 1,
      created_at: "2026-07-17T09:30:00Z",
    },
  ],
  submissions: [
    {
      id: "sub-1",
      step_instance_id: "step-1",
      status: "DONE_WITH_CONCERNS",
      exit_fields: { pr_url: "https://example/pr/1" },
      artifacts: [{ kind: "pr", url: "https://example/pr/1" }],
      gaps: [{ field: "tests" }],
      raw_summary: "shipped it",
      created_at: "2026-07-17T09:20:00Z",
    },
  ],
  verdicts: [
    {
      id: "verdict-1",
      submission_id: "sub-1",
      step_instance_id: "step-1",
      result: "pass",
      verdict_by: "system",
      evidence: { concerns: ["tests"] },
      created_at: "2026-07-17T09:20:01Z",
    },
  ],
  acceptances: [
    {
      id: "acc-1",
      step_instance_id: "step-2",
      status: "pending",
      created_at: "2026-07-17T09:30:00Z",
    },
  ],
  transitions: [
    {
      id: "tr-1",
      step_instance_id: "step-1",
      from_status: "active",
      to_status: "passed",
      attempt: 1,
      trigger_by: "system",
      created_at: "2026-07-17T09:20:01Z",
    },
    {
      id: "tr-2",
      step_instance_id: "step-2",
      from_status: "pending",
      to_status: "active",
      attempt: 1,
      trigger_by: "engine",
      created_at: "2026-07-17T09:30:00Z",
    },
  ],
};

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return rtlRender(
    <QueryClientProvider client={qc}>
      <I18nProvider locale="en" resources={RESOURCES}>
        <RunDetailPage runId="run-00000000-0000-0000-0000-000000000001" />
      </I18nProvider>
    </QueryClientProvider>,
  );
}

describe("RunDetailPage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("renders steps with submission and verdict from the trace payload", async () => {
    apiMock.getWorkflowRun.mockResolvedValue(RUN_DETAIL);
    renderPage();

    // Step card: snapshot name + status badge + submission + verdict.
    expect(await screen.findByText("Implementation")).toBeInTheDocument();
    expect(screen.getByText("Passed")).toBeInTheDocument();
    expect(screen.getByText("Done with concerns")).toBeInTheDocument();
    expect(screen.getByText("shipped it")).toBeInTheDocument();
    // exit_fields / artifacts / gaps JSON blocks render.
    expect(screen.getAllByText(/pr\/1/).length).toBeGreaterThan(0);
    expect(screen.getByText(/"field": "tests"/)).toBeInTheDocument();
    // Verdict: pass by system (the trigger attributions also say "system",
    // so assert against the set).
    expect(screen.getByText("Pass")).toBeInTheDocument();
    expect(screen.getAllByText(/system/).length).toBeGreaterThan(0);
  });

  it("renders the transition timeline in order with from → to labels", async () => {
    apiMock.getWorkflowRun.mockResolvedValue(RUN_DETAIL);
    renderPage();

    expect(await screen.findByText("Timeline")).toBeInTheDocument();
    const passed = screen.getByText("passed");
    const active = screen.getByText("active", { selector: "span.font-medium" });
    expect(passed).toBeInTheDocument();
    expect(active).toBeInTheDocument();
    // Timeline rows carry the node_key and trigger attribution.
    expect(screen.getAllByText("implement").length).toBeGreaterThan(0);
    expect(screen.getAllByText(/trigger:/).length).toBe(2);
  });

  it("highlights the pending acceptance with approve + reject controls", async () => {
    apiMock.getWorkflowRun.mockResolvedValue(RUN_DETAIL);
    renderPage();

    expect(
      await screen.findByText("This run is waiting for an acceptance decision."),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Approve" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Reject" })).toBeInTheDocument();
  });
});
