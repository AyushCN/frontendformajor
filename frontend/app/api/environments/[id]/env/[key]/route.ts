import { NextRequest, NextResponse } from "next/server";

export async function DELETE(
  req: NextRequest,
  props: { params: Promise<{ id: string; key: string }> }
) {
  const { id, key } = await props.params;
  const backend = process.env.BACKEND_URL || "http://localhost:8080";
  const cookie = req.headers.get("cookie") || "";
  const authHeader = req.headers.get("authorization") || "";

  try {
    const res = await fetch(`${backend}/api/sandboxes/${id}/env/${encodeURIComponent(key)}`, {
      method: "DELETE",
      headers: {
        cookie,
        ...(authHeader ? { authorization: authHeader } : {}),
      },
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
