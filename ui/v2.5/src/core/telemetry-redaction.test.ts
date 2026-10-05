import { describe, expect, it } from "vitest";
import { diagnosticMessage } from "./telemetry-redaction";

describe("actionable error messages without private payloads", () => {
  it.each([
    "Cannot read properties of undefined (reading 'duration')",
    "Cannot read properties of null (reading 'currentTime')",
    "Maximum call stack size exceeded",
    "Invalid array length",
    "posthog is not defined",
    "Failed to fetch",
    "useSettings must be used within a SettingsContext",
    "Not a valid funscript",
  ])("retains technical cause: %s", (message) => {
    expect(diagnosticMessage(message)).toBe(message);
  });
  it.each([
    [
      "Cannot read properties of undefined (reading 'privateTitle')",
      "Cannot read properties of undefined (reading '[property redacted]')",
    ],
    ["privateUsername is not defined", "[identifier redacted] is not defined"],
    [
      "HTTP 401: token=secret private-user /media/private.mp4",
      "HTTP 401 [response redacted]",
    ],
    [
      "Auth failed (HTTP 403): Bearer secret",
      "Auth failed (HTTP 403) [response redacted]",
    ],
    [
      "Script upload failed (HTTP 500): private title",
      "Script upload failed (HTTP 500) [response redacted]",
    ],
    [
      "Failed to fetch sprite: private@example.test",
      "Failed to fetch sprite: [response redacted]",
    ],
    [
      "Unexpected token 'p', \"private media\" is not valid JSON",
      "Invalid JSON [input redacted]",
    ],
    [
      "Unexpected non-whitespace character after JSON at position 25 (line 1 column 26)",
      "Invalid JSON at position 25 column 26 [input redacted]",
    ],
    [
      "Failed to fetch dynamically imported module: https://private/media.mp4?token=secret",
      "JavaScript chunk load failed [URL redacted]",
    ],
  ])("keeps operation while redacting dynamic leaves", (message, expected) => {
    expect(diagnosticMessage(message)).toBe(expected);
  });
  it("never interprets quoted private preview digits as a JSON position", () => {
    expect(
      diagnosticMessage(
        `Unexpected token 'a', "at position 9876" is not valid JSON`
      )
    ).toBe("Invalid JSON [input redacted]");
  });
  it("retains status from the actual StashTag space-delimited response format", () => {
    expect(
      diagnosticMessage(
        "Backend error: 503 private title /media/private.mp4 token=secret"
      )
    ).toBe("Backend error 503 [response redacted]");
  });
  it.each([
    "private.mp4 secret",
    "Jane Smith",
    "Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJwcml2YXRlIn0.secret",
    "C:\\Users\\Private\\media.mp4",
    "/mnt/user/media/private.mp4",
    "https%3A%2F%2Fprivate%2Fmedia.mp4%3Ftoken%3Dsecret",
    "token=secret\nprivate response content",
    "usePrivateUsername must be used within a PrivateTitle",
    "Failed to fetch private.mp4",
    "x".repeat(10000),
  ])("drops unknown unstructured content", (message) => {
    expect(diagnosticMessage(message)).toBe(
      "Unrecognized error text [redacted]"
    );
  });
});
