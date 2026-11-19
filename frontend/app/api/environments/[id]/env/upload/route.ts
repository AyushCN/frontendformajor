import { NextRequest, NextResponse } from "next/server";

export async function POST(
  req: NextRequest,
  props: { params: Promise<{ id: string }> }
) {
  const { id } = await props.params;
  const backend = process.env.BACKEND_URL || "http://localhost:8080";
  const cookie = req.headers.get("cookie") || "";
  const authHeader = req.headers.get("authorization") || "";

  try {
    const contentType = req.headers.get("content-type") || "";
    let body: any;
    let headers: Record<string, string> = {
      cookie,
      ...(authHeader ? { authorization: authHeader } : {}),
    };

    if (contentType.includes("multipart/form-data")) {
      const formData = await req.formData();
      body = formData;
    } else {
      headers["Content-Type"] = "application/json";
      const json = await req.json().catch(() => ({}));
      body = JSON.stringify(json);
    }

    const res = await fetch(`${backend}/api/sandboxes/${id}/env/upload`, {
      method: "POST",
      headers,
      body,
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
