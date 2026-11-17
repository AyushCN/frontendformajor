import { NextRequest, NextResponse } from "next/server";

export async function POST(
  req: NextRequest,
  props: { params: Promise<{ id: string }> }
) {
  const { id } = await props.params;
  const backend = process.env.BACKEND_URL || "http://localhost:8080";
  const cookie = req.headers.get("cookie") || "";
  const authHeader = req.headers.get("authorization") || "";

  const headers: Record<string, string> = {};
  if (cookie) headers["cookie"] = cookie;
  if (authHeader) headers["authorization"] = authHeader;

  try {
    const res = await fetch(`${backend}/api/projects/${id}/sandboxes`, {
      headers,
      cache: "no-store",
    });

    if (res.ok) {
      const data = await res.json();
      const list = Array.isArray(data) ? data : data?.sandboxes || [];

      const first = list.find((sb: any) => sb.status !== "DELETED") || list[0];
      if (first) {
        return NextResponse.json({ environmentId: first.id });
      }
    }
  } catch {
    // Fallback
  }

  return NextResponse.json(
    { error: "No active sandboxes found in this workspace project." },
    { status: 404 }
  );
}
