"use client";

import { use } from "react";
import { TemplateDetailPage } from "@multica/views/workflows/components";

export default function Page({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  const { id } = use(params);
  return <TemplateDetailPage templateId={id} />;
}
