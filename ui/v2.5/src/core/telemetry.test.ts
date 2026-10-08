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
            value: "private.mp4 token=secret",
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
            value: "private-file.mp4 token=secret",
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
      /private|secret|token=secret|https:/
    );
  });
  it("preserves unfamiliar errors, causes and typed context through reconstruction", () => {
    const safe = sanitizeTelemetry(
      event("$exception", {
        operation: "decoder.initialize",
        stage: "configure",
        component: "ScenePlayer",
        code: "E_DECODER_STATE",
        status: 503,
        retry_count: 2,
        context: {
          codec: "av1",
          decoder: { pendingFrames: 8, ready: false },
          response_body: "Jane Smith",
          path: "/mnt/private/movie.mp4",
          message: "Frame queue stalled: token=secret",
        },
        $exception_list: [
          {
            type: "DecoderStateError",
            value: "Frame queue stalled after 8 frames",
            mechanism: { type: "generic", exception_id: 0 },
          },
          {
            type: "TypeError",
            value:
              "Cannot read properties of undefined (reading 'decodeFrame')",
            mechanism: {
              type: "chained",
              source: "cause",
              parent_id: 0,
              exception_id: 1,
            },
          },
        ],
      })
    );
    expect(safe?.properties).toMatchObject({
      operation: "decoder.initialize",
      stage: "configure",
      component: "ScenePlayer",
      code: "E_DECODER_STATE",
      status: 503,
      retry_count: 2,
      context: {
        codec: "av1",
        decoder: { pendingFrames: 8, ready: false },
        response_body: "[private content redacted]",
        path: "[private content redacted]",
        message: "Frame queue stalled: token=[credential redacted]",
      },
      $exception_list: [
        {
          type: "DecoderStateError",
          value: "Frame queue stalled after 8 frames",
          mechanism: { exception_id: 0 },
        },
        {
          type: "TypeError",
          value: "Cannot read properties of undefined (reading 'decodeFrame')",
          mechanism: { source: "cause", parent_id: 0, exception_id: 1 },
        },
      ],
    });
    expect(JSON.stringify(safe)).not.toMatch(
      /Jane Smith|movie.mp4|token=secret/
    );
  });

  it("tolerates malformed exception entries and bounds frames and causes", () => {
    const frame = {
      filename: "/assets/index-Ab12Cd34.js",
      function: "decodeFrame",
    };
    const safe = sanitizeTelemetry(
      event("$exception", {
        $exception_list: [
          null,
          ...Array.from({ length: 15 }, () => ({
            value: "Decoder failed",
            stacktrace: {
              frames: [null, ...Array.from({ length: 100 }, () => frame)],
            },
          })),
        ],
      })
    );
    expect(safe?.properties.$exception_list).toHaveLength(10);
    expect(safe?.properties.$exception_list[0].stacktrace.frames).toHaveLength(
      50
    );
  });

  it("retains valid exceptions after rejected prefixes and limits accepted entries in order", () => {
    const malformed = [null, undefined, 42, "private", [], {}, { value: 42 }];
    const safe = sanitizeTelemetry(
      event("$exception", {
        $exception_list: [
          ...Array.from(
            { length: 12 },
            (_, n) => malformed[n % malformed.length]
          ),
          ...Array.from({ length: 12 }, (_, n) => ({
            type: "TypeError",
            value: `Decoder state ${n} unavailable`,
            mechanism: {
              exception_id: n,
              ...(n ? { parent_id: 0, source: "cause", type: "chained" } : {}),
            },
          })),
        ],
      })
    );
    expect(
      safe?.properties.$exception_list.map(
        (item: { value: string }) => item.value
      )
    ).toEqual(
      Array.from({ length: 10 }, (_, n) => `Decoder state ${n} unavailable`)
    );
    expect(safe?.properties.$exception_list[1].mechanism).toMatchObject({
      exception_id: 1,
      parent_id: 0,
      source: "cause",
    });
  });

  it.each([
    ["stack-only", undefined],
    ["stack-only", 42],
    ["type-only", undefined],
    ["type-only", { private: "content" }],
    ["mechanism-only", undefined],
    ["mechanism-only", null],
  ])("retains %s diagnostics with value %j", (kind, value) => {
    const frame = {
      filename: "/assets/index-Ab12Cd34.js",
      lineno: 12,
      colno: 8,
      function: "decodeFrame",
      chunk_id: "11111111-2222-4333-8444-555555555555",
    };
    const metadata =
      kind === "stack-only"
        ? { stacktrace: { frames: [frame] } }
        : kind === "type-only"
        ? { type: "DecoderStateError" }
        : {
            mechanism: {
              exception_id: 1,
              parent_id: 0,
              source: "cause",
              type: "chained",
            },
          };
    const safe = sanitizeTelemetry(
      event("$exception", {
        $exception_list: [
          { ...metadata, ...(value === undefined ? {} : { value }) },
        ],
      })
    );
    const entries = safe?.properties.$exception_list;
    expect(entries).toHaveLength(1);
    expect(entries[0]).toMatchObject({
      type: kind === "type-only" ? "DecoderStateError" : "Error",
      value: "Non-string error message [redacted]",
      mechanism: kind === "mechanism-only" ? metadata.mechanism : {},
      stacktrace: {
        type: "raw",
        frames: kind === "stack-only" ? [frame] : [],
      },
    });
  });

  it("keeps a message-less root ID linked to a child cause", () => {
    const safe = sanitizeTelemetry(
      event("$exception", {
        $exception_list: [
          { mechanism: { exception_id: 0 } },
          {
            type: "TypeError",
            value:
              "Cannot read properties of undefined (reading 'decodeFrame')",
            mechanism: { exception_id: 1, parent_id: 0, source: "cause" },
          },
        ],
      })
    );
    expect(safe?.properties.$exception_list).toMatchObject([
      {
        type: "Error",
        value: "Non-string error message [redacted]",
        mechanism: { exception_id: 0 },
      },
      {
        type: "TypeError",
        mechanism: { exception_id: 1, parent_id: 0, source: "cause" },
      },
    ]);
  });

  it("skips empty or unsafe entries before counting ten useful message-less diagnostics", () => {
    const junk = [
      null,
      undefined,
      42,
      "private",
      [],
      {},
      { value: 42 },
      { value: "" },
      { value: "   " },
      { type: "invalid private type" },
      { mechanism: { type: "private", handled: "yes", exception_id: -1 } },
      {
        stacktrace: {
          frames: [null, {}, { filename: "https://foreign.example/plugin.js" }],
        },
      },
    ];
    const useful = Array.from({ length: 12 }, (_, n) => ({
      ...(n % 2 ? { value: 42 } : {}),
      ...(n % 3 === 0
        ? {
            stacktrace: {
              frames: [{ filename: "/assets/index-Ab12Cd34.js", lineno: n }],
            },
          }
        : n % 3 === 1
        ? { type: `DecoderError${n}` }
        : { mechanism: { exception_id: n, parent_id: 0, source: "cause" } }),
    }));
    const safe = sanitizeTelemetry(
      event("$exception", {
        $exception_list: [
          ...Array.from({ length: 24 }, (_, n) => junk[n % junk.length]),
          ...useful,
        ],
      })
    );
    const entries = safe?.properties.$exception_list;
    expect(entries).toHaveLength(10);
    expect(entries.map((item: { value: string }) => item.value)).toEqual(
      Array(10).fill("Non-string error message [redacted]")
    );
    expect(
      entries.map(
        (
          item: {
            type: string;
            mechanism: { exception_id?: number };
            stacktrace: { frames: { lineno: number }[] };
          },
          n: number
        ) =>
          n % 3 === 0
            ? item.stacktrace.frames[0].lineno
            : n % 3 === 1
            ? item.type
            : item.mechanism.exception_id
      )
    ).toEqual([
      0,
      "DecoderError1",
      2,
      3,
      "DecoderError4",
      5,
      6,
      "DecoderError7",
      8,
      9,
    ]);
    expect(entries[2].mechanism).toEqual({
      exception_id: 2,
      parent_id: 0,
      source: "cause",
    });
  });

  it("retains symbolication frames after rejected prefixes and limits accepted frames in order", () => {
    const safe = sanitizeTelemetry(
      event("$exception", {
        $exception_list: [
          {
            stacktrace: {
              frames: [
                null,
                {},
                ...Array.from({ length: 50 }, () => ({
                  filename: "https://foreign.example/plugin.js",
                })),
                ...Array.from({ length: 60 }, (_, n) => ({
                  filename: "/assets/index-Ab12Cd34.js",
                  lineno: n + 1,
                  colno: 12,
                  function: "decodeFrame",
                  chunk_id: "11111111-2222-4333-8444-555555555555",
                })),
              ],
            },
          },
        ],
      })
    );
    const frames = safe?.properties.$exception_list[0].stacktrace.frames;
    expect(frames).toHaveLength(50);
    expect(frames.map((frame: { lineno: number }) => frame.lineno)).toEqual(
      Array.from({ length: 50 }, (_, n) => n + 1)
    );
    expect(frames[0]).toMatchObject({
      filename: "/assets/index-Ab12Cd34.js",
      colno: 12,
      function: "decodeFrame",
      chunk_id: "11111111-2222-4333-8444-555555555555",
    });
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
            type: "TypeError",
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
