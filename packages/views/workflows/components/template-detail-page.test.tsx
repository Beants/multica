// template-detail-page.test.tsx — the draft edit → save → publish flow with
// the API mocked at the client boundary. Covers: form seeded from the
// server payload, name edit flows into the PUT body (with nodes+edges graph
// rewrite), publish POSTs, and the malformed-fallback contract (an
// EMPTY-shaped detail — what parseWithFallback returns on drift — renders
// the not-found state instead of crashing).

import type { ReactNode } from "react";
import { describe, expect, it, beforeEach, vi } from "vitest";
import { render as rtlRender, screen, waitFor, type RenderOptions } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import { RESOURCES } from "../../locales";

const apiMock = vi.hoisted(() => ({
  getWorkflowTemplate: vi.fn(),
  updateWorkflowTemplate: vi.fn(),
  publishWorkflowTemplate: vi.fn(),
  archiveWorkflowTemplate: vi.fn(),
}));

vi.mock("@multica/core/api", () => ({ api: apiMock }));

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

// The node editor's agent dropdown reads the workspace agent list — empty
// is fine for these tests (the seeded "codex" value renders via the
// custom-value fallback option).
vi.mock("@multica/core/workspace/queries", () => ({
  agentListOptions: () => ({
    queryKey: ["agents", "ws-1"],
    queryFn: () => Promise.resolve([]),
  }),
}));

vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({
    workflows: () => "/acme/workflows",
  }),
}));

vi.mock("@multica/core/workflows/flag", () => ({
  useWorkflowEngineFlag: () => true,
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

import { TemplateDetailPage } from "./template-detail-page";

const DRAFT_DETAIL = {
  id: "tpl-1",
  key: "standard",
  name: "Standard flow",
  description: "the standard chain",
  version: 1,
  status: "draft",
  created_at: "2026-07-01T00:00:00Z",
  updated_at: "2026-07-01T00:00:00Z",
  nodes: [
    {
      id: "n1",
      node_key: "implement",
      type: "agent",
      name: "Implementation",
      config: {
        role: "executor",
        agent_selector: "codex",
        instructions: "do the work",
        exit_fields: { fields: [{ name: "pr_url", type: "string", required: true }] },
      },
    },
    { id: "n2", node_key: "acceptance", type: "acceptance", name: "Acceptance", config: {} },
    { id: "n3", node_key: "end", type: "end", name: "End", config: {} },
  ],
  edges: [
    { id: "e1", from_node_key: "implement", to_node_key: "acceptance", priority: 0 },
    { id: "e2", from_node_key: "acceptance", to_node_key: "end", priority: 0 },
  ],
};

function I18nWrapper({ children }: { children: ReactNode }) {
  return (
    <I18nProvider locale="en" resources={RESOURCES}>
      {children}
    </I18nProvider>
  );
}

function renderPage(options?: RenderOptions) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return rtlRender(
    <QueryClientProvider client={qc}>
      <I18nWrapper>
        <TemplateDetailPage templateId="tpl-1" />
      </I18nWrapper>
    </QueryClientProvider>,
    options,
  );
}

describe("TemplateDetailPage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("seeds the form from the draft detail and saves name edits with the full graph", async () => {
    apiMock.getWorkflowTemplate.mockResolvedValue(DRAFT_DETAIL);
    apiMock.updateWorkflowTemplate.mockResolvedValue({ ...DRAFT_DETAIL, name: "Renamed flow" });

    renderPage();

    const nameInput = await screen.findByLabelText("Name");
    expect(nameInput).toHaveValue("Standard flow");

    const user = userEvent.setup();
    await user.clear(nameInput);
    await user.type(nameInput, "Renamed flow");
    await user.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => expect(apiMock.updateWorkflowTemplate).toHaveBeenCalledTimes(1));
    const [id, body] = apiMock.updateWorkflowTemplate.mock.calls[0]!;
    expect(id).toBe("tpl-1");
    expect(body).toMatchObject({
      name: "Renamed flow",
      nodes: [
        { node_key: "implement", type: "agent" },
        { node_key: "acceptance", type: "acceptance" },
        { node_key: "end", type: "end" },
      ],
      edges: [
        { from_node_key: "implement", to_node_key: "acceptance" },
        { from_node_key: "acceptance", to_node_key: "end" },
      ],
    });
    // The agent node's config carries role/selector/instructions/exit_fields
    // through the form untouched.
    expect(body.nodes[0].config).toMatchObject({
      role: "executor",
      agent_selector: "codex",
      instructions: "do the work",
      exit_fields: { fields: [{ name: "pr_url", type: "string", required: true }] },
    });
  });

  it("publishes the draft via the publish endpoint", async () => {
    apiMock.getWorkflowTemplate.mockResolvedValue(DRAFT_DETAIL);
    apiMock.publishWorkflowTemplate.mockResolvedValue({ ...DRAFT_DETAIL, status: "published" });

    renderPage();

    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Publish" }));

    await waitFor(() => expect(apiMock.publishWorkflowTemplate).toHaveBeenCalledWith("tpl-1"));
  });

  it("rejects a save with an empty node name before hitting the API", async () => {
    apiMock.getWorkflowTemplate.mockResolvedValue(DRAFT_DETAIL);

    renderPage();

    const nameInputs = await screen.findAllByLabelText("Display name");
    const user = userEvent.setup();
    await user.clear(nameInputs[0]!);
    await user.click(screen.getByRole("button", { name: "Save" }));

    // Client-side validation toast; no network write.
    await waitFor(() => expect(apiMock.updateWorkflowTemplate).not.toHaveBeenCalled());
  });

  it("renders the not-found state for a malformed-fallback detail (id empty)", async () => {
    // What parseWithFallback returns when the server payload drifts —
    // the page must degrade to the not-found view, not crash.
    apiMock.getWorkflowTemplate.mockResolvedValue({
      id: "",
      key: "",
      name: "",
      description: "",
      version: 1,
      status: "draft",
      created_at: "",
      updated_at: "",
      nodes: [],
      edges: [],
    });

    renderPage();

    expect(await screen.findByText("Template not found")).toBeInTheDocument();
  });

  it("warns before saving a non-linear graph as a linear chain", async () => {
    // A branching draft (out-degree 2 on the head): the list editor can't
    // represent it, so the banner must surface before the user rewrites
    // the graph by saving.
    apiMock.getWorkflowTemplate.mockResolvedValue({
      ...DRAFT_DETAIL,
      edges: [
        { id: "e1", from_node_key: "implement", to_node_key: "acceptance", priority: 0 },
        { id: "e2", from_node_key: "implement", to_node_key: "end", priority: 1 },
        { id: "e3", from_node_key: "acceptance", to_node_key: "end", priority: 0 },
      ],
    });

    renderPage();

    expect(await screen.findByRole("alert")).toHaveTextContent(/non-linear/);
  });

  it("does not warn for a plain linear chain", async () => {
    apiMock.getWorkflowTemplate.mockResolvedValue(DRAFT_DETAIL);

    renderPage();

    await screen.findByLabelText("Name");
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("rejects a save with duplicate exit-field names before hitting the API", async () => {
    apiMock.getWorkflowTemplate.mockResolvedValue({
      ...DRAFT_DETAIL,
      nodes: [
        {
          ...DRAFT_DETAIL.nodes[0]!,
          config: {
            ...DRAFT_DETAIL.nodes[0]!.config,
            exit_fields: {
              fields: [
                { name: "pr_url", type: "string", required: true },
                { name: "pr_url", type: "string", required: false },
              ],
            },
          },
        },
        ...DRAFT_DETAIL.nodes.slice(1),
      ],
    });

    renderPage();

    await screen.findByLabelText("Name");
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => expect(apiMock.updateWorkflowTemplate).not.toHaveBeenCalled());
  });
});
