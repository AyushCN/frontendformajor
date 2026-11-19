import { describe, it, expect } from "vitest";

// Environment variable parser & serializer
export function parseEnvContent(raw: string): Record<string, string> {
  const result: Record<string, string> = {};
  const lines = raw.split("\n");

  for (const line of lines) {
    const trimmed = line.trim();
    if (!trimmed || trimmed.startsWith("#")) continue;

    const equalIndex = trimmed.indexOf("=");
    if (equalIndex === -1) continue;

    const key = trimmed.slice(0, equalIndex).trim();
    let value = trimmed.slice(equalIndex + 1).trim();

    // Strip wrapping quotes if present
    if (
      (value.startsWith('"') && value.endsWith('"')) ||
      (value.startsWith("'") && value.endsWith("'"))
    ) {
      value = value.slice(1, -1);
    }

    if (key) {
      result[key] = value;
    }
  }

  return result;
}

export function mergeEnvVars(
  existing: Record<string, string>,
  incoming: Record<string, string>,
  strategy: "merge" | "replace"
): Record<string, string> {
  if (strategy === "replace") {
    return { ...incoming };
  }
  // merge: keep existing values, only add new non-existent keys
  const merged = { ...existing };
  for (const [k, v] of Object.entries(incoming)) {
    if (!(k in merged)) {
      merged[k] = v;
    }
  }
  return merged;
}

export function maskSecretValue(value: string, isMasked: boolean): string {
  if (isMasked) {
    return "••••••••";
  }
  return value;
}

describe("Environment Variable Parsing & Merge Strategies", () => {
  it("correctly parses standard key=value pairs, ignores comments and whitespace", () => {
    const raw = `
      # Database connection
      DATABASE_URL=postgres://user:pass@localhost:5432/app
      PORT=8080

      # Secret keys
      SECRET_KEY="super-secret-123"
      ANOTHER_KEY='single-quoted-val'
      EMPTY_KEY=
    `;

    const parsed = parseEnvContent(raw);
    expect(parsed["DATABASE_URL"]).toBe(
      "postgres://user:pass@localhost:5432/app"
    );
    expect(parsed["PORT"]).toBe("8080");
    expect(parsed["SECRET_KEY"]).toBe("super-secret-123");
    expect(parsed["ANOTHER_KEY"]).toBe("single-quoted-val");
    expect(parsed["EMPTY_KEY"]).toBe("");
    expect(parsed["# Database connection"]).toBeUndefined();
  });

  it("handles merge strategy: preserves existing keys and adds missing keys", () => {
    const existing = {
      DATABASE_URL: "postgres://prod-db",
      PORT: "8080",
    };
    const incoming = {
      DATABASE_URL: "postgres://overwritten-should-be-ignored",
      API_KEY: "secret-token",
    };

    const result = mergeEnvVars(existing, incoming, "merge");
    expect(result["DATABASE_URL"]).toBe("postgres://prod-db");
    expect(result["PORT"]).toBe("8080");
    expect(result["API_KEY"]).toBe("secret-token");
  });

  it("handles replace strategy: completely overwrites existing with incoming keys", () => {
    const existing = {
      OLD_KEY: "old_val",
      ANOTHER_OLD: "val",
    };
    const incoming = {
      FRESH_KEY: "fresh_val",
    };

    const result = mergeEnvVars(existing, incoming, "replace");
    expect(result).toEqual({ FRESH_KEY: "fresh_val" });
    expect(result["OLD_KEY"]).toBeUndefined();
  });

  it("masks secret values properly when masked flag is true", () => {
    expect(maskSecretValue("very-secret-password", true)).toBe("••••••••");
    expect(maskSecretValue("very-secret-password", false)).toBe(
      "very-secret-password"
    );
  });
});
