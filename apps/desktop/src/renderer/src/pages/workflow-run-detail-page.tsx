import { useParams } from "react-router-dom";
import { RunDetailPage as WorkflowRunDetail } from "@multica/views/workflows/components";
import { useDocumentTitle } from "@/hooks/use-document-title";

export function WorkflowRunDetailPage() {
  const { id } = useParams<{ id: string }>();

  useDocumentTitle("Workflow Run");

  if (!id) return null;
  return <WorkflowRunDetail runId={id} />;
}
