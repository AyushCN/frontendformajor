import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { ApiError, apiClient } from "../api-client";

describe("api-client and ApiError", () => {
  const originalFetch = global.fetch;

  beforeEach(() => {
    vi.restoreAllMocks();
  });

  afterEach(() => {
    global.fetch = originalFetch;
  });

  it("constructs ApiError correctly with status, message and details", () => {
    const err = new ApiError(404, "Project not found", { code: "NOT_FOUND" });
    expect(err).toBeInstanceOf(Error);
    expect(err.name).toBe("ApiError");
    expect(err.status).toBe(404);
    expect(err.message).toBe("Project not found");
    expect(err.details).toEqual({ code: "NOT_FOUND" });
  });

  it("successfully parses JSON responses on 200 OK", async () => {
    const mockUser = {
      id: "u-123",
      username: "alice",
      createdAt: "2026-01-01T00:00:00Z",
    };

    global.fetch = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      headers: new Headers({ "content-type": "application/json" }),
      json: async () => mockUser,
    } as unknown as Response);

    const user = await apiClient.getCurrentUser();
    expect(user).toEqual(mockUser);
    expect(global.fetch).toHaveBeenCalledWith("/api/me", {
      credentials: "include",
      headers: { "Content-Type": "application/json" },
    });
  });

  it("throws ApiError with server error message when API responds with error JSON", async () => {
    global.fetch = vi.fn().mockResolvedValue({
      ok: false,
      status: 403,
      headers: new Headers({ "content-type": "application/json" }),
      json: async () => ({ error: "access denied: actor must be Owner or Editor" }),
    } as unknown as Response);

    await expect(apiClient.getProject("proj-forbidden")).rejects.toThrow(
      "access denied: actor must be Owner or Editor"
    );
  });

  it("throws ApiError with raw text if response is not JSON", async () => {
    global.fetch = vi.fn().mockResolvedValue({
      ok: false,
      status: 502,
      headers: new Headers({ "content-type": "text/plain" }),
      text: async () => "Bad Gateway",
    } as unknown as Response);

    await expect(apiClient.listProjects()).rejects.toThrow("Bad Gateway");
  });

  it("sends POST body correctly when creating a project", async () => {
    const mockCreated = {
      id: "proj-new",
      name: "Cool Project",
      description: "A shared project",
      isDefault: false,
      createdBy: "u-123",
      createdAt: "2026-01-01T00:00:00Z",
    };

    global.fetch = vi.fn().mockResolvedValue({
      ok: true,
      status: 201,
      headers: new Headers({ "content-type": "application/json" }),
      json: async () => mockCreated,
    } as unknown as Response);

    const proj = await apiClient.createProject({
      name: "Cool Project",
      description: "A shared project",
    });

    expect(proj).toEqual(mockCreated);
    expect(global.fetch).toHaveBeenCalledWith("/api/projects", {
      method: "POST",
      credentials: "include",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        name: "Cool Project",
        description: "A shared project",
      }),
    });
  });
});
