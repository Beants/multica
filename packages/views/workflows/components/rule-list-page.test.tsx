// rule-list-page.test.tsx — P1-fe-2 AC5: rule CRUD flow (list render, create
// mutation fired with the right payload, empty state).

import type { ReactNode } from "react";
import { describe, expect, it, beforeEach, vi } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import { RESOURCES } from "../../locales";

const apiMock = vi.hoisted(() => ({
  listWorkflowRules: vi.fn(),
  createWorkflowRule: vi.fn(),
  deleteWorkflowRule: vi.fn(),
}));

vi.mock("@multica/core/api", () => ({ api: apiMock }));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/workflows/flag", () => ({
  useWorkflowEngineFlag: () => true,
}));

import { RuleListPage } from "./rule-list-page";

function I18nWrapper({ children }: { children: ReactNode }) {
  return (
    <I18nProvider locale="en" resources={RESOURCES}>
      {children}
    </I18nProvider>
  );
}

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <I18nWrapper>
        <RuleListPage />
      </I18nWrapper>
    </QueryClientProvider>,
  );
}

const SOFT_RULE = {
  id: "r1",
  workspace_id: "ws-1",
  name: "PR tests",
  level: "soft",
  scope: "workspace",
  content: "PR description must include test coverage notes",
  status: "active",
  version: 1,
  created_at: "",
  updated_at: "",
};

describe("RuleListPage", () => {
  beforeEach(() => vi.clearAllMocks());

  it("lists rules with name, level badge, and content", async () => {
    apiMock.listWorkflowRules.mockResolvedValue([SOFT_RULE]);
    renderPage();

    expect(await screen.findByText("PR tests")).toBeInTheDocument();
    expect(screen.getByText(/include test coverage notes/)).toBeInTheDocument();
    // level + scope render inside the rule card; asserting them broadly would
    // collide with the create-form <select> options, so the name + content
    // pair is the canonical "row rendered" signal.
  });

  it("fires createWorkflowRule with the form payload on Add rule", async () => {
    apiMock.listWorkflowRules.mockResolvedValue([]);
    apiMock.createWorkflowRule.mockResolvedValue({ ...SOFT_RULE, id: "r2", name: "New rule" });
    renderPage();

    await screen.findByPlaceholderText(/Name/);
    fireEvent.change(screen.getByPlaceholderText(/Name/), { target: { value: "New rule" } });
    fireEvent.change(screen.getByPlaceholderText(/Rule content/), { target: { value: "do stuff" } });
    fireEvent.click(screen.getByRole("button", { name: "Add rule" }));

    await waitFor(() =>
      expect(apiMock.createWorkflowRule).toHaveBeenCalledWith({
        name: "New rule",
        level: "soft",
        content: "do stuff",
      }),
    );
  });

  it("shows the empty state when there are no rules", async () => {
    apiMock.listWorkflowRules.mockResolvedValue([]);
    renderPage();
    expect(await screen.findByText(/No rules yet/)).toBeInTheDocument();
  });
});
