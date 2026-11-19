import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { fetchWithAuth } from "../auth";

describe("fetchWithAuth", () => {
  const originalFetch = global.fetch;

  beforeEach(() => {
    vi.restoreAllMocks();
  });

  afterEach(() => {
    global.fetch = originalFetch;
  });

  it("includes credentials and returns parsed JSON on 200 OK", async () => {
    const mockData = { id: "123", status: "running" };

    global.fetch = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => mockData,
    } as unknown as Response);

    const result = await fetchWithAuth("/api/test");
    expect(result).toEqual(mockData);
    expect(global.fetch).toHaveBeenCalledWith("/api/test", {
      credentials: "include",
    });
  });

  it("throws Unauthorized on 401 response", async () => {
    global.fetch = vi.fn().mockResolvedValue({
      ok: false,
      status: 401,
      json: async () => ({ error: "not signed in" }),
    } as unknown as Response);

    await expect(fetchWithAuth("/api/me")).rejects.toThrow("Unauthorized");
  });

  it("unwraps custom server error message on 400 or 500", async () => {
    global.fetch = vi.fn().mockResolvedValue({
      ok: false,
      status: 400,
      json: async () => ({ error: "destination projectId is required" }),
    } as unknown as Response);

    await expect(fetchWithAuth("/api/transfer")).rejects.toThrow(
      "destination projectId is required"
    );
  });
});
