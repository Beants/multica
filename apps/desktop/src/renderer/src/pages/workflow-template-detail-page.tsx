import { useParams } from "react-router-dom";
import { TemplateDetailPage as WorkflowTemplateDetail } from "@multica/views/workflows/components";
import { useDocumentTitle } from "@/hooks/use-document-title";

export function WorkflowTemplateDetailPage() {
  const { id } = useParams<{ id: string }>();

  useDocumentTitle("Workflows");

  if (!id) return null;
  return <WorkflowTemplateDetail templateId={id} />;
}
