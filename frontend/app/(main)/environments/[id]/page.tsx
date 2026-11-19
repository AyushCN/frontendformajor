import { redirect } from "next/navigation";

export default async function EnvironmentPageRedirect({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  const { id } = await params;
  redirect(`/sandboxes/${id}`);
}
