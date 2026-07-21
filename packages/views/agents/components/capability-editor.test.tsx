// capability-editor.test.tsx — P1-fe-3 AC: capability CRUD flow.

import { describe, expect, it, beforeEach, vi } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

const apiMock = vi.hoisted(() => ({
  listAgentCapabilities: vi.fn(),
  createAgentCapability: vi.fn(),
  deleteAgentCapability: vi.fn(),
}));

vi.mock("@multica/core/api", () => ({ api: apiMock }));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));

import { CapabilityEditor } from "./capability-editor";

function renderEditor() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <CapabilityEditor agentId="agent-1" />
    </QueryClientProvider>,
  );
}

describe("CapabilityEditor", () => {
  beforeEach(() => vi.clearAllMocks());

  it("lists capabilities with key + proficiency", async () => {
    apiMock.listAgentCapabilities.mockResolvedValue([
      { id: "c1", agent_id: "agent-1", capability_key: "python", proficiency: 80, created_at: "", updated_at: "" },
    ]);
    renderEditor();

    expect(await screen.findByText("python")).toBeInTheDocument();
    expect(screen.getByText(/proficiency 80/)).toBeInTheDocument();
  });

  it("fires createAgentCapability with key + proficiency on Add", async () => {
    apiMock.listAgentCapabilities.mockResolvedValue([]);
    apiMock.createAgentCapability.mockResolvedValue({
      id: "c2", agent_id: "agent-1", capability_key: "refactor", proficiency: 50, created_at: "", updated_at: "",
    });
    renderEditor();

    await screen.findByPlaceholderText(/capability_key/);
    fireEvent.change(screen.getByPlaceholderText(/capability_key/), { target: { value: "refactor" } });
    fireEvent.click(screen.getByRole("button", { name: "Add" }));

    await waitFor(() =>
      expect(apiMock.createAgentCapability).toHaveBeenCalledWith("agent-1", {
        capability_key: "refactor",
        proficiency: 50,
      }),
    );
  });

  it("shows the empty state when no capabilities are labeled", async () => {
    apiMock.listAgentCapabilities.mockResolvedValue([]);
    renderEditor();
    expect(await screen.findByText(/No capabilities labeled/)).toBeInTheDocument();
  });
});
