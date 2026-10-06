import { afterEach, describe, expect, it, vi } from "vitest";
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
  afterEach(() => vi.unstubAllEnvs());

  it.each(["$pageview", "$identify", "$exception"])(
    "supplies build-owned app/release metadata for %s",
    (name) => {
      vi.stubEnv("VITE_APP_STASH_VERSION", "v0.1.0");
      vi.stubEnv("VITE_APP_GITHASH", "890c5f1");
      const sanitized = sanitizeTelemetry(
        event(name, {
          $user_id: "42",
          $set: { user_role: "ADMIN" },
          $app_namespace: "private namespace",
          $app_version: "private version",
          $app_build: "private build",
          $exception_release: "private release",
          $exception_release_version: "private revision",
        })
      );
      expect(sanitized?.properties).toMatchObject({
        $app_namespace: "vexxx-ui",
        $app_version: "v0.1.0",
        $app_build: "890c5f1",
      });
      expect(sanitized?.properties).not.toHaveProperty("$exception_release");
      expect(sanitized?.properties).not.toHaveProperty(
        "$exception_release_version"
      );
      expect(JSON.stringify(sanitized)).not.toContain("private");
    }
  );

  it("identifies unversioned development builds without using caller metadata", () => {
    vi.stubEnv("VITE_APP_STASH_VERSION", "");
    vi.stubEnv("VITE_APP_GITHASH", "");
    expect(sanitizeTelemetry(event("$exception"))?.properties).toMatchObject({
      $app_namespace: "vexxx-ui",
      $app_version: "development",
      $app_build: "development",
      app_revision: "development",
    });
  });

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
        $user_id: "42",
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
                  filename: `${window.location.origin}/assets/index-Ab12Cd34.js?token=secret`,
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
        filename: "/assets/index-Ab12Cd34.js",
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
  it("always supplies a safe function string for every retained frame", () => {
    const names = [
      undefined,
      null,
      "",
      "?",
      42,
      "https://private.example/file.js?token=secret",
      "private/media.mp4",
      "x".repeat(152),
      "render",
      "t.<anonymous>",
    ];
    const sanitized = sanitizeTelemetry(
      event("$exception", {
        $exception_list: [
          {
            type: "TypeError",
            value: "private-file.mp4 secret",
            stacktrace: {
              frames: names.map((name) => ({
                filename: `${window.location.origin}/assets/index-Ab12Cd34.js?token=secret`,
                function: name,
                lineno: 124,
                colno: 29373,
              })),
            },
          },
        ],
      })
    );
    const frames = sanitized?.properties.$exception_list[0].stacktrace.frames;
    expect(frames.map((frame: { function: string }) => frame.function)).toEqual(
      ["?", "?", "?", "?", "?", "?", "?", "?", "render", "t.<anonymous>"]
    );
    for (const frame of frames) {
      expect(Object.prototype.hasOwnProperty.call(frame, "function")).toBe(
        true
      );
      expect(typeof frame.function).toBe("string");
      expect(frame).toMatchObject({
        filename: "/assets/index-Ab12Cd34.js",
        platform: "web:javascript",
        in_app: true,
        lineno: 124,
        colno: 29373,
      });
    }
    expect(JSON.stringify(sanitized)).not.toMatch(
      /private|secret|token=|https:/
    );
  });
  it("rejects identities without the authenticated numeric user ID and role", () => {
    for (const properties of [
      {},
      { $user_id: "42", $set: { user_role: "unknown" } },
      { $user_id: "43", $set: { user_role: "ADMIN" } },
      {
        distinct_id: "00000000-0000-4000-8000-000000000000",
        $set: { user_role: "ADMIN" },
      },
    ]) {
      expect(sanitizeTelemetry(event("$identify", properties))).toBeNull();
    }
  });
  it("rejects foreign, plugin and unversioned private asset names", () => {
    const sanitized = sanitizeTelemetry(
      event("$exception", {
        $exception_list: [
          {
            stacktrace: {
              frames: [
                {
                  filename: "https://foreign.example/assets/index-Ab12Cd34.js",
                },
                {
                  filename: `${window.location.origin}/plugin/private/assets/index-Ab12Cd34.js`,
                },
                {
                  filename: `${window.location.origin}/assets/private-media.js`,
                },
                {
                  filename: `${window.location.origin}/assets/nested/index-Ab12Cd34.js`,
                },
              ],
            },
          },
        ],
      })
    );
    expect(sanitized?.properties.$exception_list[0].stacktrace.frames).toEqual(
      []
    );
  });
  it("supports the configured reverse proxy base without retaining its name", () => {
    const base = document.createElement("base");
    base.href = "/private-installation/";
    document.head.appendChild(base);
    try {
      const sanitized = sanitizeTelemetry(
        event("$exception", {
          $exception_list: [
            {
              stacktrace: {
                frames: [
                  {
                    filename: `${window.location.origin}/private-installation/assets/index-Ab12Cd34.js`,
                  },
                  {
                    filename: `${window.location.origin}/assets/index-Ab12Cd34.js`,
                  },
                ],
              },
            },
          ],
        })
      );
      expect(
        sanitized?.properties.$exception_list[0].stacktrace.frames
      ).toEqual([
        {
          filename: "/assets/index-Ab12Cd34.js",
          platform: "web:javascript",
          in_app: true,
          function: "?",
        },
      ]);
    } finally {
      base.remove();
    }
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
