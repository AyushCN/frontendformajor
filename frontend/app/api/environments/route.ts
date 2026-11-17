import { NextRequest, NextResponse } from "next/server";

export async function GET(req: NextRequest) {
  const backend = process.env.BACKEND_URL || "http://localhost:8080";
  const cookie = req.headers.get("cookie") || "";
  const authHeader = req.headers.get("authorization") || "";
  const projectId = req.nextUrl.searchParams.get("projectId");

  const headers: Record<string, string> = {};
  if (cookie) headers["cookie"] = cookie;
  if (authHeader) headers["authorization"] = authHeader;

  try {
    if (projectId) {
      const pRes = await fetch(`${backend}/api/projects/${projectId}/sandboxes`, {
        headers,
        cache: "no-store",
      });
      if (pRes.ok) {
        const pData = await pRes.json();
        const list = Array.isArray(pData) ? pData : pData?.sandboxes || [];
        return NextResponse.json({ sandboxes: list });
      }
    }

    const res = await fetch(`${backend}/api/sandboxes`, {
      headers,
      cache: "no-store",
    });

    if (!res.ok) {
      const data = await res.json().catch(() => ({}));
      return NextResponse.json(data, { status: res.status });
    }

    const data = await res.json();
    const list = Array.isArray(data) ? data : data?.sandboxes || [];
    return NextResponse.json({ sandboxes: list });
  } catch (err: unknown) {
    return NextResponse.json(
      { error: (err as Error).message },
      { status: 500 }
    );
  }
}
