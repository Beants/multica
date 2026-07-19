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
