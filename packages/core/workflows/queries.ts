import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

export const workflowKeys = {
  all: (wsId: string) => ["workflows", wsId] as const,
  templates: (wsId: string) => [...workflowKeys.all(wsId), "templates"] as const,
  template: (wsId: string, id: string) =>
    [...workflowKeys.all(wsId), "templates", id] as const,
  hooks: (wsId: string) => [...workflowKeys.all(wsId), "hooks"] as const,
  runs: (wsId: string) => [...workflowKeys.all(wsId), "runs"] as const,
  run: (wsId: string, id: string) =>
    [...workflowKeys.all(wsId), "runs", id] as const,
  diagnosis: (wsId: string, id: string) =>
    [...workflowKeys.run(wsId, id), "diagnosis"] as const,
  rules: (wsId: string) => [...workflowKeys.all(wsId), "rules"] as const,
  agentCapabilities: (wsId: string, agentId: string) =>
    [...workflowKeys.all(wsId), "agents", agentId, "capabilities"] as const,
};

export function workflowTemplateListOptions(wsId: string) {
  return queryOptions({
    queryKey: workflowKeys.templates(wsId),
    queryFn: () => api.listWorkflowTemplates(),
  });
}

export function workflowTemplateDetailOptions(wsId: string, id: string) {
  return queryOptions({
    queryKey: workflowKeys.template(wsId, id),
    queryFn: () => api.getWorkflowTemplate(id),
  });
}

export function workflowHookListOptions(wsId: string) {
  return queryOptions({
    queryKey: workflowKeys.hooks(wsId),
    queryFn: () => api.listWorkflowHooks(),
  });
}

export function workflowRunListOptions(wsId: string) {
  return queryOptions({
    queryKey: workflowKeys.runs(wsId),
    queryFn: () => api.listWorkflowRuns(),
  });
}

export function workflowRunDetailOptions(wsId: string, id: string) {
  return queryOptions({
    queryKey: workflowKeys.run(wsId, id),
    queryFn: () => api.getWorkflowRun(id),
  });
}

// P1-fe-1: per-step seven-element diagnosis for the run detail "Diagnosis" tab.
export function workflowRunDiagnosisOptions(wsId: string, id: string) {
  return queryOptions({
    queryKey: workflowKeys.diagnosis(wsId, id),
    queryFn: () => api.getRunDiagnosis(id),
  });
}

// P1-fe-2: Rules asset list (workspace-scoped).
export function workflowRuleListOptions(wsId: string) {
  return queryOptions({
    queryKey: workflowKeys.rules(wsId),
    queryFn: () => api.listWorkflowRules(),
  });
}

// P1-fe-3: agent capability labels (P1-7 dispatch data).
export function agentCapabilityListOptions(wsId: string, agentId: string) {
  return queryOptions({
    queryKey: workflowKeys.agentCapabilities(wsId, agentId),
    queryFn: () => api.listAgentCapabilities(agentId),
  });
}
