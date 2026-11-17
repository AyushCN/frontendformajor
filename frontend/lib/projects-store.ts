// Type definitions for Project, Collaborator, and Invitation structures.
// Authoritative persistence is managed exclusively by the PostgreSQL database.

export interface ProjectRepository {
  id: string;
  name: string;
  gitUrl: string;
  defaultBranch: string;
}

export interface StoredCollaborator {
  id: string;
  userId: string;
  role: "OWNER" | "EDITOR" | "VIEWER";
  status: "ACTIVE" | "PENDING";
  acceptedAt?: string;
  user?: {
    username: string;
    email?: string;
    avatarUrl?: string;
  };
}

export interface StoredProject {
  id: string;
  name: string;
  description: string;
  createdAt: string;
  isDefault?: boolean;
  createdBy?: string;
  collaborators: StoredCollaborator[];
  sandboxIds: string[];
  repositories?: ProjectRepository[];
}

export interface StoredInvitation {
  id: string;
  projectId: string;
  projectName: string;
  inviterId: string;
  inviterName: string;
  inviteeId: string;
  inviteeIdentifier: string;
  role: "EDITOR" | "VIEWER";
  status: "PENDING" | "ACCEPTED" | "REJECTED" | "REVOKED" | "EXPIRED";
  createdAt: string;
  expiresAt: string;
  acceptedAt?: string;
}
