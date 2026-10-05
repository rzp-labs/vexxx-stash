import { describe, expect, it, vi } from "vitest";
import {
  sanitizeTelemetry,
  telemetryConfig,
  initializeTelemetry,
} from "./telemetry";
import posthog from "posthog-js/no-external";

vi.mock("posthog-js/no-external", () => ({ default: { init: vi.fn() } }));
vi.mock("posthog-js/dist/exception-autocapture", () => ({}));

const event = (name: string, properties: Record<string, unknown> = {}) => ({
  uuid: "00000000-0000-4000-8000-000000000000",
  event: name,
  properties: {
    token: "public-ingestion-token",
    distinct_id: "42",
    ...properties,
  },
});

describe("private media telemetry", () => {
  it("does not require an external service for development", () => {
    vi.stubEnv("VITE_PUBLIC_POSTHOG_PROJECT_TOKEN", "");
    vi.stubEnv("VITE_PUBLIC_POSTHOG_HOST", "");
    initializeTelemetry();
    expect(posthog.init).not.toHaveBeenCalled();
    vi.unstubAllEnvs();
  });
  it("disables DOM, replay, remote configuration and respects DNT", () => {
    expect(telemetryConfig).toMatchObject({
      autocapture: false,
      disable_session_recording: true,
      capture_performance: false,
      advanced_disable_flags: true,
      respect_dnt: true,
      disable_external_dependency_loading: true,
    });
  });
  it("drops unknown events and sensitive properties including nested person properties", () => {
    expect(
      sanitizeTelemetry(event("$autocapture", { text: "private" }))
    ).toBeNull();
    const sanitized = sanitizeTelemetry({
      ...event("$identify", {
        $current_url: "https://private/media.mp4?token=secret",
        $referrer: "private",
        secret: "secret",
        $set: { username: "private", user_role: "ADMIN" },
        $set_once: { filename: "private.mp4" },
      }),
      $set: { username: "private", user_role: "ADMIN" },
      $set_once: { secret: "secret" },
    });
    expect(sanitized?.properties.distinct_id).toBe("42");
    expect(sanitized?.properties.$set).toEqual({ user_role: "ADMIN" });
    expect(JSON.stringify(sanitized)).not.toMatch(
      /private|secret|filename|current_url|referrer|set_once/
    );
  });
  it("keeps bundle stack positions and chunk IDs while redacting messages and media frames", () => {
    const chunk = "11111111-2222-4333-8444-555555555555";
    const sanitized = sanitizeTelemetry(
      event("$exception", {
        $exception_list: [
          {
            type: "TypeError",
            value: "private.mp4 secret",
            stacktrace: {
              frames: [
                {
                  filename:
                    "https://private-host/assets/index-Ab12.js?token=secret",
                  lineno: 12,
                  colno: 45,
                  function: "render",
                  chunk_id: chunk,
                  context_line: "secret",
                  vars: { media: "private" },
                },
                {
                  filename: "https://private-host/private.mp4?token=secret",
                  lineno: 1,
                },
              ],
            },
          },
        ],
        $exception_message: "private.mp4",
        react_component_stack: "private.mp4",
      })
    );
    expect(sanitized?.properties.$exception_list[0].stacktrace.frames).toEqual([
      {
        filename: "/assets/index-Ab12.js",
        platform: "web:javascript",
        in_app: true,
        lineno: 12,
        colno: 45,
        function: "render",
        chunk_id: chunk,
      },
    ]);
    expect(JSON.stringify(sanitized)).not.toMatch(
      /private|secret|context_line|react_component_stack/
    );
  });
  it("categorizes page views without IDs, queries, filenames or installation prefix", () => {
    window.history.replaceState(
      {},
      "",
      "/private-prefix/scenes/42?search=private.mp4&token=secret"
    );
    const sanitized = sanitizeTelemetry(
      event("$pageview", { $current_url: window.location.href })
    );
    expect(sanitized?.properties.screen).toBe("scenes");
    expect(JSON.stringify(sanitized)).not.toMatch(/private|secret|current_url/);
  });
});
