import { describe, it, expect } from "vitest";

// Slug normalization matching backend NormalizeSlug logic
export function normalizeSlug(input: string): string {
  if (!input) return "sandbox";
  let s = input.toLowerCase();
  // Replace non-alphanumeric characters with hyphens
  s = s.replace(/[^a-z0-9]+/g, "-");
  // Trim hyphens from ends
  s = s.replace(/^-+|-+$/g, "");
  // Cap at 40 characters
  if (s.length > 40) {
    s = s.slice(0, 40).replace(/-+$/, "");
  }
  return s || "sandbox";
}

// Generate consistent preview hostname
export function buildPreviewHostname(
  slug: string,
  id: string,
  baseDomain = "127.0.0.1.nip.io"
): string {
  const shortId = id.replace(/-/g, "").slice(0, 8);
  const normalized = normalizeSlug(slug);
  return `${normalized}-${shortId}.${baseDomain}`;
}

describe("Sandbox Naming & Slug Normalization", () => {
  it("normalizes display names into URL-safe slugs", () => {
    expect(normalizeSlug("Weather API Development")).toBe(
      "weather-api-development"
    );
  });

  it("replaces special characters and symbols", () => {
    expect(normalizeSlug("My App! @v2 #prod $test %^&*()")).toBe(
      "my-app-v2-prod-test"
    );
  });

  it("handles consecutive spaces and hyphens cleanly", () => {
    expect(normalizeSlug("  hello---world   test--123  ")).toBe(
      "hello-world-test-123"
    );
  });

  it("caps slug at 40 characters without trailing hyphen", () => {
    const longName =
      "this is an extremely long sandbox display name that exceeds forty characters by far";
    const slug = normalizeSlug(longName);
    expect(slug.length).toBeLessThanOrEqual(40);
    expect(slug).toBe("this-is-an-extremely-long-sandbox-displa");
  });

  it("falls back to 'sandbox' for empty or symbol-only inputs", () => {
    expect(normalizeSlug("")).toBe("sandbox");
    expect(normalizeSlug("   !!!   ")).toBe("sandbox");
  });

  it("generates deterministic collision-resistant preview hostname", () => {
    const hostname = buildPreviewHostname(
      "Weather API Development",
      "a81f2c99-1234-5678-abcd-000000000000",
      "127.0.0.1.nip.io"
    );
    expect(hostname).toBe("weather-api-development-a81f2c99.127.0.0.1.nip.io");
  });
});
