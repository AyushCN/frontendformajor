import { NextRequest, NextResponse } from "next/server";

interface RegisteredUser {
  id: string;
  username: string;
  email: string;
  displayName: string;
  avatarUrl?: string;
  isMember?: boolean;
  isPending?: boolean;
}

export async function GET(req: NextRequest) {
  const url = new URL(req.url);
  const q = (url.searchParams.get("q") || "").trim().toLowerCase();
  const projectId = url.searchParams.get("projectId") || "";

  if (q.length < 2) {
    return NextResponse.json({ users: [] });
  }

  const backend = process.env.BACKEND_URL || "http://localhost:8080";
  const cookie = req.headers.get("cookie") || "";
  const authHeader = req.headers.get("authorization") || "";

  const headers: Record<string, string> = {};
  if (cookie) headers["cookie"] = cookie;
  if (authHeader) headers["authorization"] = authHeader;

  // 1. Identify current authenticated user to exclude them from search results
  let currentUsername = "";
  let currentEmail = "";
  let currentUserId = "";

  try {
    const meRes = await fetch(`${backend}/api/me`, {
      headers,
      cache: "no-store",
    });
    if (meRes.ok) {
      const me = await meRes.json();
      currentUsername = (me.username || "").toLowerCase();
      currentEmail = (me.email || "").toLowerCase();
      currentUserId = (me.id || "").toLowerCase();
    }
  } catch {
    // Ignore error
  }

  // 2. Fetch registered DB users from backend
  const allUsersMap = new Map<string, RegisteredUser>();

  try {
    const remoteRes = await fetch(`${backend}/api/users/search?q=${encodeURIComponent(q)}`, {
      headers,
      cache: "no-store",
    });
    if (remoteRes.ok) {
      const remoteData = await remoteRes.json();
      const remoteUsers: any[] = Array.isArray(remoteData)
        ? remoteData
        : remoteData?.users || [];
      for (const ru of remoteUsers) {
        if (ru && (ru.username || ru.email)) {
          const key = (ru.username || ru.email).toLowerCase();
          if (!allUsersMap.has(key)) {
            allUsersMap.set(key, {
              id: ru.id || "",
              username: ru.username || key,
              email: ru.email || "",
              displayName: ru.displayName || ru.username || key,
              avatarUrl: ru.avatarUrl || "",
            });
          }
        }
      }
    }
  } catch {
    // Backend search error
  }

  // 3. Filter by query string and exclude current user
  const matchedUsers: RegisteredUser[] = [];
  for (const u of allUsersMap.values()) {
    const uName = (u.username || "").toLowerCase();
    const uEmail = (u.email || "").toLowerCase();
    const uDisplay = (u.displayName || "").toLowerCase();
    const uId = (u.id || "").toLowerCase();

    if (
      (currentUsername && uName === currentUsername) ||
      (currentEmail && uEmail === currentEmail) ||
      (currentUserId && uId === currentUserId)
    ) {
      continue;
    }

    if (uName.includes(q) || uEmail.includes(q) || uDisplay.includes(q)) {
      matchedUsers.push(u);
    }
  }

  // 4. If projectId provided, check for existing membership and pending invitations from backend
  const memberIdentifiers = new Set<string>();
  const pendingIdentifiers = new Set<string>();

  if (projectId) {
    try {
      // Check members
      const memRes = await fetch(`${backend}/api/projects/${projectId}/members`, {
        headers,
        cache: "no-store",
      });
      if (memRes.ok) {
        const memData = await memRes.json();
        const members: any[] = Array.isArray(memData) ? memData : memData?.members || [];
        for (const m of members) {
          if (m.user?.username) memberIdentifiers.add(m.user.username.toLowerCase());
          if (m.user?.email) memberIdentifiers.add(m.user.email.toLowerCase());
          if (m.username) memberIdentifiers.add(m.username.toLowerCase());
          if (m.email) memberIdentifiers.add(m.email.toLowerCase());
          if (m.userId) memberIdentifiers.add(m.userId.toLowerCase());
          if (m.user_id) memberIdentifiers.add(m.user_id.toLowerCase());
        }
      }

      // Check pending invitations
      const invRes = await fetch(`${backend}/api/projects/${projectId}/invitations`, {
        headers,
        cache: "no-store",
      });
      if (invRes.ok) {
        const invData = await invRes.json();
        const invitations: any[] = Array.isArray(invData) ? invData : invData?.invitations || [];
        const now = new Date();
        for (const inv of invitations) {
          if (inv.status === "PENDING" && (!inv.expires_at || new Date(inv.expires_at) > now)) {
            const iden = (inv.invitee_identifier || inv.inviteeIdentifier || "").toLowerCase();
            if (iden) pendingIdentifiers.add(iden);
            const invId = (inv.invitee_id || inv.inviteeId || "").toLowerCase();
            if (invId) pendingIdentifiers.add(invId);
          }
        }
      }
    } catch {
      // Ignore
    }
  }

  // 5. Sanitize results (exclude any sensitive fields)
  const results = matchedUsers.slice(0, 10).map((u) => {
    const uName = u.username.toLowerCase();
    const uEmail = u.email.toLowerCase();
    const uId = u.id.toLowerCase();

    const isMember =
      memberIdentifiers.has(uName) ||
      memberIdentifiers.has(uEmail) ||
      (uId ? memberIdentifiers.has(uId) : false);

    const isPending =
      pendingIdentifiers.has(uName) ||
      pendingIdentifiers.has(uEmail) ||
      (uId ? pendingIdentifiers.has(uId) : false);

    return {
      id: u.id,
      username: u.username,
      email: u.email,
      displayName: u.displayName || u.username,
      avatarUrl: u.avatarUrl || "",
      isMember,
      isPending,
    };
  });

  return NextResponse.json({ users: results });
}
