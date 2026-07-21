// dashboard-page.test.tsx — P2-4 AC5: mock metrics + events → render.

import { describe, expect, it, beforeEach, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

const apiMock = vi.hoisted(() => ({
  listWorkflowMetrics: vi.fn(),
  listWorkflowEvents: vi.fn(),
}));

vi.mock("@multica/core/api", () => ({ api: apiMock }));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/workflows/flag", () => ({ useWorkflowEngineFlag: () => true }));

import { DashboardPage } from "./dashboard-page";

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <DashboardPage />
    </QueryClientProvider>,
  );
}

describe("DashboardPage", () => {
  beforeEach(() => vi.clearAllMocks());

  it("renders event distribution bars + recent events feed", async () => {
    apiMock.listWorkflowMetrics.mockResolvedValue([
      { event_type: "run.paused", event_count: 5 },
      { event_type: "task.dispatch", event_count: 2 },
    ]);
    apiMock.listWorkflowEvents.mockResolvedValue([
      { id: "e1", event_type: "run.paused", occurred_at: "2026-07-21T10:00:00Z", actor_type: "system" },
    ]);
    renderPage();

    expect((await screen.findAllByText("run.paused")).length).toBeGreaterThan(0);
    expect(screen.getAllByText("task.dispatch").length).toBeGreaterThan(0);
    // bar count labels
    expect(screen.getByText("5")).toBeInTheDocument();
    expect(screen.getByText("2")).toBeInTheDocument();
    // event feed header
    expect(screen.getByText(/Recent events \(1\)/)).toBeInTheDocument();
  });

  it("shows empty states when no data", async () => {
    apiMock.listWorkflowMetrics.mockResolvedValue([]);
    apiMock.listWorkflowEvents.mockResolvedValue([]);
    renderPage();
    expect(await screen.findByText(/No events yet/)).toBeInTheDocument();
    expect(screen.getByText(/^No events\.$/)).toBeInTheDocument();
  });
});
