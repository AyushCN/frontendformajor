// Type definitions for Project, Collaborator, and Invitation structures.
// Authoritative persistence is managed exclusively by the PostgreSQL database.

export interface ProjectRepository {
  id: string;
  name: string;
  gitUrl: string;
  defaultBranch: string;
}

export interface StoredProject {
  id: string;
  name: string;
  description: string;
  createdAt: string;
  isDefault?: boolean;
  createdBy?: string;
  collaborators: any[];
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
