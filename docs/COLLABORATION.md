# Project Collaboration & Container Sharing Specification

This document details the multi-tenant collaboration model, role-based access control (RBAC), and invitation workflow in **API Sandbox Links**.

---

## 1. The Two Types of Spaces

The platform establishes a strict architectural boundary between two types of development spaces:

### A. Default Project (Personal Space)
Every registered user automatically receives their own private Default Project (`default-project`):
- **Exclusive Ownership**: Owned exclusively by the authenticated user (e.g., `AyushCN`).
- **Private Environments**: Contains personal development containers and workspaces.
- **Strictly Non-Shareable**: Cannot receive collaboration invitations and cannot be shared with other platform users.
- **Hidden Sharing Controls**: Sharing buttons ("Invite Collaborator") are completely removed from the UI.
- **API Guard**: Any invitation requests targeting `default-project` are rejected with `HTTP 403 Forbidden`.

### B. Shareable Projects (Collaborative Space)
Users can create separate, dedicated projects to collaborate with teammates:
- **Identifiable Creator & Owner**: Created independently with customizable project names and descriptions.
- **Collaborator Management**: The project owner can invite registered platform users via email or username.
- **Multi-Environment Scoping**: Holds microservices, containers, and repositories accessible to all approved members.
- **Audit Tracking**: Collaborator actions (invitations, role changes, removals) are tracked and displayed on the Team Activity feed.

---

## 2. Role-Based Access Control (RBAC) Matrix

| Permission / Action | Owner | Editor | Viewer |
|---|:---:|:---:|:---:|
| **View Live Preview & Logs** | ✅ | ✅ | ✅ |
| **Inspect File Tree & Code** | ✅ | ✅ | ✅ |
| **Edit Code in Monaco IDE** | ✅ | ✅ | ❌ |
| **Execute Terminal Commands** | ✅ | ✅ | ❌ |
| **Commit Changes & Push Branches** | ✅ | ✅ | ❌ |
| **Deploy & Restart Sandbox** | ✅ | ✅ | ❌ |
| **Invite Team Collaborators** | ✅ | ❌ | ❌ |
| **Modify Member Roles** | ✅ | ❌ | ❌ |
| **Transfer Sandbox Ownership** | ✅ | ❌ | ❌ |
| **Delete Project or Sandbox** | ✅ | ❌ | ❌ |

---

## 3. Collaboration Invitation Flow

```
   Project Owner                       Invited User
        │                                   │
        ├────── Send Invitation ───────────▶│ (Status: PENDING)
        │       (Select Role: EDITOR)       │
        │                                   ├─▶ Option A: Accept Invite ──▶ (Status: ACTIVE, Role: EDITOR)
        │                                   │                                 Gain immediate access to project
        │                                   │
        │                                   └─▶ Option B: Decline Invite ─▶ (Status: REJECTED)
        │                                                                     Access denied
        │
        └────── Revoke Invitation ─────────── (Status: REVOKED)
```

1. **Invite Generation**:
   - The owner opens the Collaborators tab on a shareable project.
   - Enters recipient username/email and selects role (`EDITOR` or `VIEWER`).
   - System prevents self-invitation (`HTTP 400`) and duplicate active invitations (`HTTP 409`).
2. **Delivery & Review**:
   - The invited user views incoming invitations under `/projects` or `/api/invitations`.
3. **Acceptance**:
   - Recipient accepts the invite; status transitions to `ACCEPTED` / `ACTIVE`.
   - Recipient is immediately granted access according to their role.
4. **Member Removal / Leaving**:
   - The project owner can remove any non-owner collaborator at any time.
   - Any collaborator can independently choose to leave a shared project.

---

## 4. Multi-User Real-Time Awareness

- **Collaborator Presence**: Active team members working simultaneously are badged with real-time presence indicators.
- **Concurrent Idle Prevention**: If User A is actively coding in Monaco while User B is idle, User A's activity automatically keeps the sandbox runtime awake for the entire team.
