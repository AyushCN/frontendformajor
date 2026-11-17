import { NextRequest, NextResponse } from "next/server";

export async function GET(req: NextRequest) {
  const backend = process.env.BACKEND_URL || "http://localhost:8080";
  const cookie = req.headers.get("cookie") || "";
  const authHeader = req.headers.get("authorization") || "";

  const headers: Record<string, string> = {};
  if (cookie) headers["cookie"] = cookie;
  if (authHeader) headers["authorization"] = authHeader;

  try {
    const res = await fetch(`${backend}/api/invitations`, {
      headers,
      cache: "no-store",
    });
    const data = await res.json().catch(() => ([]));
    return NextResponse.json(data, { status: res.status });
  } catch {
    return NextResponse.json([], { status: 503 });
  }
}
