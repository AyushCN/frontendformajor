import { NextRequest, NextResponse } from "next/server";

export async function GET(req: NextRequest) {
  const backend = process.env.BACKEND_URL || "http://localhost:8080";
  const cookie = req.headers.get("cookie") || "";
  const authHeader = req.headers.get("authorization") || "";

  const headers: Record<string, string> = {};
  if (cookie) headers["cookie"] = cookie;
  if (authHeader) headers["authorization"] = authHeader;

  try {
    const res = await fetch(`${backend}/api/projects`, {
      headers,
      cache: "no-store",
    });
    const data = await res.json().catch(() => ([]));
    return NextResponse.json(data, { status: res.status });
  } catch {
    return NextResponse.json([], { status: 503 });
  }
}

export async function POST(req: NextRequest) {
  const backend = process.env.BACKEND_URL || "http://localhost:8080";
  const cookie = req.headers.get("cookie") || "";
  const authHeader = req.headers.get("authorization") || "";

  const headers: Record<string, string> = { "Content-Type": "application/json" };
  if (cookie) headers["cookie"] = cookie;
  if (authHeader) headers["authorization"] = authHeader;

  try {
    const body = await req.json().catch(() => ({}));
    const res = await fetch(`${backend}/api/projects`, {
      method: "POST",
      headers,
      body: JSON.stringify(body),
    });
    const data = await res.json().catch(() => ({}));
    return NextResponse.json(data, { status: res.status });
  } catch (err: unknown) {
    return NextResponse.json(
      { error: (err as Error).message },
      { status: 500 }
    );
  }
}
