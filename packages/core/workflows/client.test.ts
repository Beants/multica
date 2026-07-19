// client.test.ts — proves the ApiClient workflow methods hold the CLAUDE.md
// contract end-to-end: malformed JSON from the wire degrades to EMPTY_*
// fallbacks (never throws), and well-formed payloads round-trip through the
// lenient schemas. Complements schemas.test.ts (schema-level) with the
// client-level parseWithFallback wiring.

import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient } from "../api/client";

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("ApiClient workflow methods (malformed responses)", () => {
  it("returns empty fallbacks for structurally broken payloads", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation(() => Promise.resolve(jsonResponse({ unexpected: true }))),
    );
    const client = new ApiClient("https://api.example.test");

    await expect(client.listWorkflowTemplates()).resolves.toEqual([]);
    await expect(client.listWorkflowHooks()).resolves.toEqual([]);
    await expect(client.listWorkflowRuns()).resolves.toEqual([]);
    await expect(client.getWorkflowTemplate("t1")).resolves.toMatchObject({ id: "", nodes: [] });
    await expect(client.getWorkflowRun("r1")).resolves.toMatchObject({ id: "", steps: [] });
  });

  it("returns empty fallbacks for wrong-shaped list payloads", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation(() => Promise.resolve(jsonResponse([{ id: 123 }]))),
    );
    const client = new ApiClient("https://api.example.test");

    await expect(client.listWorkflowTemplates()).resolves.toEqual([]);
    await expect(client.listWorkflowRuns()).resolves.toEqual([]);
  });

  it("round-trips well-formed payloads", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((url: string) => {
        if (url.endsWith("/api/workflow-runs/r1")) {
          return Promise.resolve(
            jsonResponse({
              id: "r1",
              template_id: "t1",
              status: "waiting_acceptance",
              steps: [{ id: "s1", node_key: "accept", status: "active", attempt: 1 }],
              acceptances: [{ id: "a1", step_instance_id: "s1", status: "pending" }],
            }),
          );
        }
        return Promise.resolve(jsonResponse([{ id: "t1", key: "standard", name: "Standard" }]));
      }),
    );
    const client = new ApiClient("https://api.example.test");

    const templates = await client.listWorkflowTemplates();
    expect(templates[0]).toMatchObject({ key: "standard", status: "draft" });

    const run = await client.getWorkflowRun("r1");
    expect(run.status).toBe("waiting_acceptance");
    expect(run.steps[0]).toMatchObject({ node_key: "accept" });
    expect(run.acceptances[0]?.status).toBe("pending");
  });

  it("sends the reject payload through and parses the decision", async () => {
    const fetchMock = vi.fn().mockImplementation(() =>
      Promise.resolve(jsonResponse({ run_id: "r1", acceptance_id: "a1", status: "rejected" })),
    );
    vi.stubGlobal("fetch", fetchMock);
    const client = new ApiClient("https://api.example.test");

    const decision = await client.rejectWorkflowAcceptance("r1", {
      reject_to_node_key: "impl",
      reason: "needs rework",
    });
    expect(decision).toMatchObject({ run_id: "r1", status: "rejected" });

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("https://api.example.test/api/workflow-runs/r1/acceptance/reject");
    expect(init.method).toBe("POST");
    expect(JSON.parse(String(init.body))).toEqual({
      reject_to_node_key: "impl",
      reason: "needs rework",
    });
  });
});
