import { NextRequest, NextResponse } from "next/server";

export async function DELETE(
  req: NextRequest,
  props: { params: Promise<{ id: string; inviteId: string }> }
) {
  const { inviteId } = await props.params;
  const backend = process.env.BACKEND_URL || "http://localhost:8080";
  const cookie = req.headers.get("cookie") || "";
  const authHeader = req.headers.get("authorization") || "";

  const headers: Record<string, string> = { "Content-Type": "application/json" };
  if (cookie) headers["cookie"] = cookie;
  if (authHeader) headers["authorization"] = authHeader;

  try {
    const res = await fetch(`${backend}/api/invitations/${inviteId}/revoke`, {
      method: "POST",
      headers,
    });
    const data = await res.json().catch(() => ({}));
    return NextResponse.json(data, { status: res.status });
  } catch (err: unknown) {
    return NextResponse.json({ error: (err as Error).message }, { status: 500 });
  }
}

export async function POST(
  req: NextRequest,
  props: { params: Promise<{ id: string; inviteId: string }> }
) {
  const { inviteId } = await props.params;
  const backend = process.env.BACKEND_URL || "http://localhost:8080";
  const cookie = req.headers.get("cookie") || "";
  const authHeader = req.headers.get("authorization") || "";

  const headers: Record<string, string> = { "Content-Type": "application/json" };
  if (cookie) headers["cookie"] = cookie;
  if (authHeader) headers["authorization"] = authHeader;

  try {
    const body = await req.json().catch(() => ({}));
    const action = body.action || new URL(req.url).searchParams.get("action") || "resend";
    const endpoint = action === "revoke" ? "revoke" : "resend";

    const res = await fetch(`${backend}/api/invitations/${inviteId}/${endpoint}`, {
      method: "POST",
      headers,
    });
    const data = await res.json().catch(() => ({}));
    return NextResponse.json(data, { status: res.status });
  } catch (err: unknown) {
    return NextResponse.json({ error: (err as Error).message }, { status: 500 });
  }
}
