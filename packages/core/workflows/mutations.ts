import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { useWorkspaceId } from "../hooks";
import { workflowKeys } from "./queries";
import type {
  CreateWorkflowHookRequest,
  CreateWorkflowTemplateRequest,
  RejectAcceptanceRequest,
  UpdateWorkflowTemplateRequest,
} from "./types";

export function useCreateWorkflowTemplate() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (data: CreateWorkflowTemplateRequest) =>
      api.createWorkflowTemplate(data),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: workflowKeys.templates(wsId) });
    },
  });
}

export function useUpdateWorkflowTemplate() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: ({ id, ...data }: { id: string } & UpdateWorkflowTemplateRequest) =>
      api.updateWorkflowTemplate(id, data),
    onSettled: (_data, _err, vars) => {
      qc.invalidateQueries({ queryKey: workflowKeys.template(wsId, vars.id) });
      qc.invalidateQueries({ queryKey: workflowKeys.templates(wsId) });
    },
  });
}

export function usePublishWorkflowTemplate() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (id: string) => api.publishWorkflowTemplate(id),
    onSettled: (_data, _err, id) => {
      qc.invalidateQueries({ queryKey: workflowKeys.template(wsId, id) });
      qc.invalidateQueries({ queryKey: workflowKeys.templates(wsId) });
    },
  });
}

export function useArchiveWorkflowTemplate() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (id: string) => api.archiveWorkflowTemplate(id),
    onSettled: (_data, _err, id) => {
      qc.invalidateQueries({ queryKey: workflowKeys.template(wsId, id) });
      qc.invalidateQueries({ queryKey: workflowKeys.templates(wsId) });
    },
  });
}

export function useCreateWorkflowHook() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (data: CreateWorkflowHookRequest) => api.createWorkflowHook(data),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: workflowKeys.hooks(wsId) });
    },
  });
}

export function useDisableWorkflowHook() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (id: string) => api.disableWorkflowHook(id),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: workflowKeys.hooks(wsId) });
    },
  });
}

// Acceptance decisions move the run out of waiting_acceptance (approve →
// advance, reject → targeted rework), so both the run detail and the runs
// list go stale. The run detail carries the acceptance row itself.
export function useApproveAcceptance() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (runId: string) => api.approveWorkflowAcceptance(runId),
    onSettled: (_data, _err, runId) => {
      qc.invalidateQueries({ queryKey: workflowKeys.run(wsId, runId) });
      qc.invalidateQueries({ queryKey: workflowKeys.runs(wsId) });
    },
  });
}

export function useRejectAcceptance() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: ({ runId, ...data }: { runId: string } & RejectAcceptanceRequest) =>
      api.rejectWorkflowAcceptance(runId, data),
    onSettled: (_data, _err, vars) => {
      qc.invalidateQueries({ queryKey: workflowKeys.run(wsId, vars.runId) });
      qc.invalidateQueries({ queryKey: workflowKeys.runs(wsId) });
    },
  });
}
