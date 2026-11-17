import { NextRequest, NextResponse } from "next/server";

export async function POST(
  req: NextRequest,
  props: { params: Promise<{ id: string }> }
) {
  const { id } = await props.params;
  const backend = process.env.BACKEND_URL || "http://localhost:8080";
  const cookie = req.headers.get("cookie") || "";
  const authHeader = req.headers.get("authorization") || "";

  const headers: Record<string, string> = { "Content-Type": "application/json" };
  if (cookie) headers["cookie"] = cookie;
  if (authHeader) headers["authorization"] = authHeader;

  try {
    const body = await req.json().catch(() => ({}));
    const sandboxId = body.sandboxId || body.sandbox_id;
    if (!sandboxId) {
      return NextResponse.json({ error: "sandboxId is required" }, { status: 400 });
    }

    const res = await fetch(`${backend}/api/projects/${id}/sandboxes`, {
      method: "POST",
      headers,
      body: JSON.stringify({ sandbox_id: sandboxId }),
    });
    const data = await res.json().catch(() => ({}));
    return NextResponse.json(data, { status: res.status });
  } catch (err: unknown) {
    return NextResponse.json({ error: (err as Error).message }, { status: 500 });
  }
}
