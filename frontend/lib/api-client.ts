// Centralized typed API client for API Sandbox Links.
// All requests are typed and return validated backend responses.
// Errors include HTTP status codes and backend error messages.

export class ApiError extends Error {
  public status: number;
  public details?: unknown;

  constructor(status: number, message: string, details?: unknown) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.details = details;
  }
}

export interface User {
  id: string;
  githubId?: number;
  username: string;
  email?: string;
  avatarUrl?: string;
  createdAt: string;
}

export type ProjectRole = "OWNER" | "EDITOR" | "VIEWER";
export type InvitationStatus = "PENDING" | "ACCEPTED" | "REJECTED" | "REVOKED" | "EXPIRED";

export interface ProjectMember {
  id: string;
  userId: string;
  role: ProjectRole;
  status: "ACTIVE" | "PENDING";
  acceptedAt?: string;
  user?: {
    username: string;
    email?: string;
    avatarUrl?: string;
  };
}

export interface ProjectRepository {
  id: string;
  name: string;
  gitUrl: string;
  defaultBranch: string;
}

export interface Project {
  id: string;
  name: string;
  description: string;
  createdAt: string;
  updatedAt?: string;
  isDefault: boolean;
  createdBy: string;
  collaborators?: ProjectMember[];
  sandboxIds?: string[];
  repositories?: ProjectRepository[];
}

export interface Invitation {
  id: string;
  projectId: string;
  projectName: string;
  inviterId: string;
  inviterName: string;
  inviteeId: string;
  inviteeIdentifier: string;
  role: "EDITOR" | "VIEWER";
  status: InvitationStatus;
  createdAt: string;
  expiresAt: string;
  acceptedAt?: string;
}

export type SandboxStatus =
  | "PENDING"
  | "BUILDING"
  | "RUNNING"
  | "HIBERNATED"
  | "EXPIRED"
  | "FAILED"
  | "DELETED";

export interface Sandbox {
  id: string;
  projectId?: string;
  name?: string;
  repoUrl: string;
  branch: string;
  commitHash?: string;
  builder?: string;
  status: SandboxStatus;
  subdomain: string;
  createdAt: string;
  expiresAt?: string;
  lastRequestAt?: string;
  parentSandboxId?: string;
  databaseEngine?: string;
  lifecycleVersion?: number;
}

export interface FileNode {
  name: string;
  path: string;
  isDir: boolean;
  size?: number;
  children?: FileNode[];
}

export interface FileContent {
  path: string;
  content: string;
  binary: boolean;
  size: number;
}

export interface SaveFileResult {
  success: boolean;
  path: string;
  cowForkedSandboxId?: string;
  cowForkedSubdomain?: string;
}

export interface CommitPushResult {
  commitHash: string;
  branch: string;
  redeployTriggered: boolean;
  cowForkedSandboxId?: string;
}

export interface LifecycleStatus {
  status: SandboxStatus;
  subdomain: string;
  expiresAt?: string;
  isWaking: boolean;
  lastRequestAt?: string;
}

export interface CreateProjectRequest {
  name: string;
  description?: string;
}

export interface UpdateProjectRequest {
  name?: string;
  description?: string;
}

export interface CreateInvitationRequest {
  inviteeIdentifier: string;
  role: "EDITOR" | "VIEWER";
}

export interface CreateSandboxRequest {
  projectId?: string;
  repoUrl: string;
  branch?: string;
  builder?: string;
  databaseEngine?: string;
}

// Low-level fetch wrapper with typed error propagation
async function request<T>(
  path: string,
  options: RequestInit = {}
): Promise<{ data: T; headers: Headers }> {
  const res = await fetch(path, {
    ...options,
    credentials: "include",
    headers: {
      "Content-Type": "application/json",
      ...(options.headers || {}),
    },
  });

  const contentType = res.headers.get("content-type") || "";
  let payload: unknown;
  if (contentType.includes("application/json")) {
    payload = await res.json().catch(() => ({}));
  } else {
    payload = await res.text().catch(() => "");
  }

  if (!res.ok) {
    let message = `Request failed with status ${res.status}`;
    if (typeof payload === "object" && payload !== null && "error" in payload) {
      message = String((payload as { error: unknown }).error);
    } else if (typeof payload === "string" && payload.trim()) {
      message = payload.trim();
    }
    throw new ApiError(res.status, message, payload);
  }

  return { data: payload as T, headers: res.headers };
}

export const apiClient = {
  // --- Auth & User ---
  async getCurrentUser(): Promise<User> {
    const { data } = await request<User>("/api/me");
    return data;
  },

  async searchUsers(query: string): Promise<User[]> {
    const { data } = await request<User[]>(
      `/api/users/search?q=${encodeURIComponent(query)}`
    );
    return data;
  },

  // --- Projects ---
  async listProjects(): Promise<Project[]> {
    const { data } = await request<Project[]>("/api/projects");
    return data;
  },

  async getProject(id: string): Promise<Project> {
    const { data } = await request<Project>(`/api/projects/${id}`);
    return data;
  },

  async createProject(req: CreateProjectRequest): Promise<Project> {
    const { data } = await request<Project>("/api/projects", {
      method: "POST",
      body: JSON.stringify(req),
    });
    return data;
  },

  async updateProject(id: string, req: UpdateProjectRequest): Promise<Project> {
    const { data } = await request<Project>(`/api/projects/${id}`, {
      method: "PATCH",
      body: JSON.stringify(req),
    });
    return data;
  },

  async deleteProject(id: string): Promise<{ success: boolean }> {
    const { data } = await request<{ success: boolean }>(`/api/projects/${id}`, {
      method: "DELETE",
    });
    return data;
  },

  // --- Members & Invitations ---
  async listProjectMembers(projectId: string): Promise<ProjectMember[]> {
    const { data } = await request<ProjectMember[]>(`/api/projects/${projectId}/members`);
    return data;
  },

  async inviteMember(
    projectId: string,
    req: CreateInvitationRequest
  ): Promise<Invitation> {
    const { data } = await request<Invitation>(`/api/projects/${projectId}/invite`, {
      method: "POST",
      body: JSON.stringify(req),
    });
    return data;
  },

  async removeMember(
    projectId: string,
    userId: string
  ): Promise<{ success: boolean }> {
    const { data } = await request<{ success: boolean }>(
      `/api/projects/${projectId}/collaborators/${userId}`,
      { method: "DELETE" }
    );
    return data;
  },

  async updateMemberRole(
    projectId: string,
    userId: string,
    role: ProjectRole
  ): Promise<{ success: boolean }> {
    const { data } = await request<{ success: boolean }>(
      `/api/projects/${projectId}/collaborators/${userId}`,
      {
        method: "PATCH",
        body: JSON.stringify({ role }),
      }
    );
    return data;
  },

  async listUserInvitations(): Promise<Invitation[]> {
    const { data } = await request<Invitation[]>("/api/user/invitations");
    return data;
  },

  async acceptInvitation(id: string): Promise<{ success: boolean; projectId: string }> {
    const { data } = await request<{ success: boolean; projectId: string }>(
      `/api/invitations/${id}/accept`,
      { method: "POST" }
    );
    return data;
  },

  async rejectInvitation(id: string): Promise<{ success: boolean }> {
    const { data } = await request<{ success: boolean }>(
      `/api/invitations/${id}/reject`,
      { method: "POST" }
    );
    return data;
  },

  // --- Sandboxes ---
  async listSandboxes(projectId?: string): Promise<Sandbox[]> {
    const path = projectId
      ? `/api/environments?projectId=${encodeURIComponent(projectId)}`
      : "/api/environments";
    const { data } = await request<{ sandboxes: Sandbox[] } | Sandbox[]>(path);
    if (Array.isArray(data)) {
      return data;
    }
    return data.sandboxes || [];
  },

  async getSandbox(id: string): Promise<Sandbox> {
    const { data } = await request<Sandbox>(`/api/environments/${id}`);
    return data;
  },

  async createSandbox(req: CreateSandboxRequest): Promise<Sandbox> {
    const { data } = await request<Sandbox>("/api/environments", {
      method: "POST",
      body: JSON.stringify(req),
    });
    return data;
  },

  async destroySandbox(id: string): Promise<{ success: boolean }> {
    const { data } = await request<{ success: boolean }>(
      `/api/environments/${id}`,
      { method: "DELETE" }
    );
    return data;
  },

  async restoreSandbox(id: string): Promise<Sandbox> {
    const { data } = await request<Sandbox>(
      `/api/environments/${id}/restore`,
      { method: "POST" }
    );
    return data;
  },

  async forkSandbox(id: string): Promise<Sandbox> {
    const { data } = await request<Sandbox>(
      `/api/environments/${id}/fork`,
      { method: "POST" }
    );
    return data;
  },

  async getLifecycleStatus(id: string): Promise<LifecycleStatus> {
    const { data } = await request<LifecycleStatus>(
      `/api/environments/${id}/lifecycle/status`
    );
    return data;
  },

  // --- File Workspace ---
  async getFileTree(sandboxId: string): Promise<FileNode[]> {
    const { data } = await request<{ files: FileNode[] } | FileNode[]>(
      `/api/environments/${sandboxId}/files`
    );
    if (Array.isArray(data)) {
      return data;
    }
    return data.files || [];
  },

  async getFileContent(sandboxId: string, filePath: string): Promise<FileContent> {
    const { data } = await request<FileContent>(
      `/api/environments/${sandboxId}/file?path=${encodeURIComponent(filePath)}`
    );
    return data;
  },

  async saveFile(
    sandboxId: string,
    filePath: string,
    content: string
  ): Promise<SaveFileResult> {
    const { data, headers } = await request<{ success: boolean; path: string }>(
      `/api/environments/${sandboxId}/file`,
      {
        method: "PUT",
        body: JSON.stringify({ path: filePath, content }),
      }
    );
    return {
      success: data.success,
      path: data.path || filePath,
      cowForkedSandboxId: headers.get("x-cow-forked-sandbox-id") || undefined,
      cowForkedSubdomain: headers.get("x-cow-forked-subdomain") || undefined,
    };
  },

  async commitAndPush(
    sandboxId: string,
    message: string
  ): Promise<CommitPushResult> {
    const { data, headers } = await request<CommitPushResult>(
      `/api/environments/${sandboxId}/git/commit`,
      {
        method: "POST",
        body: JSON.stringify({ message }),
      }
    );
    return {
      ...data,
      cowForkedSandboxId: headers.get("x-cow-forked-sandbox-id") || undefined,
    };
  },
};

