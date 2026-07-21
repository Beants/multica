// diagnosis-panel.test.tsx — P1-fe-1 AC5: mock the diagnosis API and assert
// the seven-element render (failed step card with failure_type + reason +
// retry arc; all-OK collapse; disabled → null).

import { describe, expect, it, beforeEach, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

const apiMock = vi.hoisted(() => ({
  getRunDiagnosis: vi.fn(),
}));

vi.mock("@multica/core/api", () => ({ api: apiMock }));

import { DiagnosisPanel } from "./diagnosis-panel";

function renderPanel(overrides: { enabled?: boolean; wsId?: string; runId?: string } = {}) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <DiagnosisPanel wsId="ws-1" runId="run-1" enabled={true} {...overrides} />
    </QueryClientProvider>,
  );
}

describe("DiagnosisPanel", () => {
  beforeEach(() => vi.clearAllMocks());

  it("renders failed step with failure_type, reason, and attempt arc", async () => {
    apiMock.getRunDiagnosis.mockResolvedValue({
      run_id: "run-1",
      run_status: "running",
      steps: [
        {
          step_id: "s1",
          node_key: "work",
          run_id: "run-1",
          attempt: 2,
          max_attempts: 3,
          final_status: "failed",
          ok: false,
          failure_type: "fail",
          reason: "OOM killed by kernel",
          transitions: [
            { id: "t1", step_instance_id: "s1", from_status: "running", to_status: "failed", attempt: 2, trigger_by: "verdict", created_at: "" },
          ],
        },
        {
          step_id: "s2",
          node_key: "review",
          run_id: "run-1",
          attempt: 1,
          final_status: "passed",
          ok: true,
          transitions: [],
        },
      ],
    });

    renderPanel();

    expect(await screen.findByText("Diagnosis · 1 failed step(s)")).toBeInTheDocument();
    expect(screen.getByText("work")).toBeInTheDocument();
    expect(screen.getByText(/OOM killed by kernel/)).toBeInTheDocument();
    expect(screen.getByText(/attempt 2\/3/)).toBeInTheDocument();
    expect(screen.getByText(/running→failed/)).toBeInTheDocument();
    // passed step is collapsed (not rendered).
    expect(screen.queryByText("review")).not.toBeInTheDocument();
  });

  it("collapses to all-OK when every step is ok", async () => {
    apiMock.getRunDiagnosis.mockResolvedValue({
      run_id: "run-1",
      run_status: "running",
      steps: [
        { step_id: "s1", node_key: "work", run_id: "run-1", attempt: 1, final_status: "passed", ok: true, transitions: [] },
      ],
    });

    renderPanel();

    expect(await screen.findByText(/all 1 step\(s\) OK/)).toBeInTheDocument();
  });

  it("renders nothing when the flag is off (enabled=false)", () => {
    apiMock.getRunDiagnosis.mockResolvedValue({ run_id: "run-1", run_status: "running", steps: [] });
    renderPanel({ enabled: false });
    expect(screen.queryByText("Diagnosis")).not.toBeInTheDocument();
  });
});
