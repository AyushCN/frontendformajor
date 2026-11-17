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
    let inviteeId = body.inviteeId || body.userId;

    // If identifier (username or email) is provided instead of inviteeId, lookup via backend
    if (!inviteeId && body.identifier) {
      const searchRes = await fetch(`${backend}/api/users/search?q=${encodeURIComponent(body.identifier.trim())}`, {
        headers,
        cache: "no-store",
      });
      if (searchRes.ok) {
        const searchData = await searchRes.json();
        const users = searchData.users || [];
        const match = users.find(
          (u: any) =>
            u.username.toLowerCase() === body.identifier.trim().toLowerCase() ||
            (u.email && u.email.toLowerCase() === body.identifier.trim().toLowerCase())
        );
        if (match) {
          inviteeId = match.id;
        }
      }
    }

    if (!inviteeId) {
      return NextResponse.json(
        { error: "Valid registered user could not be found for the provided identifier" },
        { status: 404 }
      );
    }

    const payload = {
      inviteeId,
      role: body.role || "EDITOR",
    };

    const res = await fetch(`${backend}/api/projects/${id}/invitations`, {
      method: "POST",
      headers,
      body: JSON.stringify(payload),
    });
    const data = await res.json().catch(() => ({}));

    if (!res.ok) {
      return NextResponse.json(data, { status: res.status });
    }

    return NextResponse.json({
      success: true,
      message: "Collaboration invitation sent successfully",
      invitation: data.invitation || data,
      collaborator: data.invitation || data,
    }, { status: 201 });
  } catch (err: unknown) {
    return NextResponse.json(
      { error: (err as Error).message },
      { status: 500 }
    );
  }
}
