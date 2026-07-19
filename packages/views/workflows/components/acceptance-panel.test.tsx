// acceptance-panel.test.tsx — the reject-to-node flow: open the reject form,
// pick a rework target (only nodes that ran are offered — the server 400s
// unknown targets), submit with a reason, assert the API payload. Also
// covers the approve path and the disabled-without-input guard.

import type { ReactNode } from "react";
import { describe, expect, it, beforeEach, vi } from "vitest";
import { render as rtlRender, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import { RESOURCES } from "../../locales";

const apiMock = vi.hoisted(() => ({
  approveWorkflowAcceptance: vi.fn(),
  rejectWorkflowAcceptance: vi.fn(),
}));

vi.mock("@multica/core/api", () => ({ api: apiMock }));

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

// Base UI Select is a portal-heavy composite; swap it for a native <select>
// so the reject-target interaction is testable in jsdom. The option set and
// the onValueChange contract (what the panel actually owns) stay real.
vi.mock("@multica/ui/components/ui/select", () => ({
  Select: ({
    value,
    onValueChange,
    children,
  }: {
    value: string;
    onValueChange: (v: string) => void;
    children: ReactNode;
  }) => (
    <select
      aria-label="Rework from node"
      value={value}
      onChange={(e) => onValueChange(e.target.value)}
    >
      {children}
    </select>
  ),
  SelectTrigger: () => null,
  SelectValue: () => null,
  SelectContent: ({ children }: { children: ReactNode }) => <>{children}</>,
  SelectItem: ({ value, children }: { value: string; children: ReactNode }) => (
    <option value={value}>{children}</option>
  ),
}));

import { AcceptancePanel } from "./acceptance-panel";
import type { WorkflowRunDetail, WorkflowStep } from "@multica/core/workflows/types";

const RUN: WorkflowRunDetail = {
  id: "run-1",
  template_id: "tpl-1",
  status: "waiting_acceptance",
  source_type: "hook",
  source_id: "REQ-42",
  intake_issue_id: null,
  started_at: "2026-07-17T09:00:00Z",
  updated_at: "2026-07-17T10:00:00Z",
  template_snapshot: {
    template_id: "tpl-1",
    key: "standard",
    version: 1,
    nodes: [
      { node_key: "plan", type: "agent", name: "Planning", config: {} },
      { node_key: "implement", type: "agent", name: "Implementation", config: {} },
      { node_key: "acceptance", type: "acceptance", name: "Acceptance", config: {} },
    ],
    edges: [
      { from_node_key: "plan", to_node_key: "implement", priority: 0 },
      { from_node_key: "implement", to_node_key: "acceptance", priority: 0 },
    ],
  },
  steps: [],
  submissions: [],
  verdicts: [],
  acceptances: [
    {
      id: "acc-1",
      step_instance_id: "step-3",
      status: "pending",
      created_at: "2026-07-17T09:30:00Z",
    },
  ],
  transitions: [],
};

const STEPS: WorkflowStep[] = [
  { id: "step-1", node_key: "plan", status: "passed", attempt: 1, created_at: "2026-07-17T09:01:00Z" },
  { id: "step-2", node_key: "implement", status: "passed", attempt: 1, created_at: "2026-07-17T09:10:00Z" },
  { id: "step-3", node_key: "acceptance", status: "active", attempt: 1, created_at: "2026-07-17T09:30:00Z" },
];

function renderPanel() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return rtlRender(
    <QueryClientProvider client={qc}>
      <I18nProvider locale="en" resources={RESOURCES}>
        <AcceptancePanel run={RUN} steps={STEPS} />
      </I18nProvider>
    </QueryClientProvider>,
  );
}

describe("AcceptancePanel", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("approves via the approve endpoint", async () => {
    apiMock.approveWorkflowAcceptance.mockResolvedValue({
      run_id: "run-1",
      acceptance_id: "acc-1",
      status: "approved",
    });
    renderPanel();

    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Approve" }));

    await waitFor(() =>
      expect(apiMock.approveWorkflowAcceptance).toHaveBeenCalledWith("run-1"),
    );
  });

  it("rejects with the selected rework node and reason", async () => {
    apiMock.rejectWorkflowAcceptance.mockResolvedValue({
      run_id: "run-1",
      acceptance_id: "acc-1",
      status: "rejected",
    });
    renderPanel();

    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Reject" }));

    // Submit stays disabled until both target and reason are present.
    const submit = screen.getByRole("button", { name: "Reject and request rework" });
    expect(submit).toBeDisabled();

    // The picker offers only nodes that ran — excluding the acceptance node
    // itself (reworking "into" the pending acceptance makes no sense).
    const picker = screen.getByLabelText("Rework from node");
    const optionValues = Array.from(picker.querySelectorAll("option")).map(
      (o) => o.getAttribute("value"),
    );
    expect(optionValues).toEqual(["plan", "implement"]);

    await user.selectOptions(picker, "implement");
    await user.type(
      screen.getByPlaceholderText("What must change before this can pass?"),
      "tests are failing",
    );
    expect(submit).toBeEnabled();
    await user.click(submit);

    await waitFor(() =>
      expect(apiMock.rejectWorkflowAcceptance).toHaveBeenCalledWith("run-1", {
        reject_to_node_key: "implement",
        reason: "tests are failing",
      }),
    );
  });

  it("renders decided acceptances as history with reject context", async () => {
    const decidedRun: WorkflowRunDetail = {
      ...RUN,
      acceptances: [
        {
          id: "acc-0",
          step_instance_id: "step-3",
          status: "rejected",
          reject_to_node_key: "implement",
          reject_reason: "not good enough",
          decided_at: "2026-07-17T11:00:00Z",
          created_at: "2026-07-17T09:30:00Z",
        },
      ],
    };
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    rtlRender(
      <QueryClientProvider client={qc}>
        <I18nProvider locale="en" resources={RESOURCES}>
          <AcceptancePanel run={decidedRun} steps={STEPS} />
        </I18nProvider>
      </QueryClientProvider>,
    );

    expect(screen.getByText("Rejected")).toBeInTheDocument();
    expect(screen.getByText(/not good enough/)).toBeInTheDocument();
    expect(screen.getByText("implement")).toBeInTheDocument();
    // No pending controls when nothing awaits a decision.
    expect(screen.queryByRole("button", { name: "Approve" })).not.toBeInTheDocument();
  });
});
