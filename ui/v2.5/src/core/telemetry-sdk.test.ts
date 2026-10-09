import { afterEach, describe, expect, it, vi } from "vitest";
import { PostHog, CaptureResult } from "posthog-js/no-external";
import { telemetryConfig, sanitizeTelemetry } from "./telemetry";
// @ts-expect-error Node-only test helper; UI types intentionally exclude Node globals.
import { createRequire } from "node:module";
// @ts-expect-error Node-only evidence writer; never included in the production bundle.
import { writeFileSync } from "node:fs";
// @ts-expect-error Node-only isolated execution of the installed injection snippet.
import { runInNewContext } from "node:vm";
const require = createRequire(import.meta.url);
const { createChunkIdSnippet } = createRequire(
  require.resolve("@posthog/rollup-plugin")
)("@posthog/plugin-utils") as {
  createChunkIdSnippet: (chunk: string, release?: string) => string;
};

// Exercise the installed parser and production before_send, then intercept the
// request handed to SDK transport. No telemetry leaves this offline test.
describe("pinned PostHog error parser", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    delete (globalThis as typeof globalThis & { _posthogChunkIds?: unknown })
      ._posthogChunkIds;
    delete (globalThis as typeof globalThis & { _posthogReleaseId?: unknown })
      ._posthogReleaseId;
    vi.unstubAllEnvs();
  });
  it("retains symbolication metadata from an actual SDK exception without sending", async () => {
    vi.stubEnv("VITE_APP_STASH_VERSION", "v0.2.1");
    vi.stubEnv("VITE_APP_GITHASH", "ca01e624a3e8ba001dc8f8081968b2c467aa929f");
    const fetch = vi
      .spyOn(globalThis, "fetch")
      .mockRejectedValue(new Error("network forbidden"));
    const xhr = vi
      .spyOn(XMLHttpRequest.prototype, "send")
      .mockImplementation(() => {
        throw new Error("network forbidden");
      });
    const captured: CaptureResult[] = [];
    const sdk = new PostHog();
    const beforeSend: CaptureResult[] = [];
    vi.spyOn(sdk, "_send_retriable_request").mockImplementation((request) => {
      captured.push(request.data as CaptureResult);
    });
    sdk.init("test-public-project-token", {
      ...telemetryConfig,
      before_send: (event) => {
        if (event) beforeSend.push(event);
        return sanitizeTelemetry(event);
      },
      api_host: "https://example.invalid",
      persistence: "memory",
      bootstrap: { distinctID: "00000000-0000-4000-8000-000000000000" },
      capture_pageview: false,
      capture_exceptions: false,
      request_batching: false,
    });
    sdk.identify("42", { user_role: "ADMIN" });
    expect(captured).toHaveLength(1);
    expect(captured[0].properties).toMatchObject({
      distinct_id: "42",
      $user_id: "42",
      $set: { user_role: "ADMIN" },
      $app_namespace: "vexxx-ui",
      $app_version: "v0.2.1",
      $app_build: "ca01e624a3e8ba001dc8f8081968b2c467aa929f",
    });
    captured.length = 0;
    const chunk = "11111111-2222-4333-8444-555555555555";
    const stack = `TypeError: Cannot read properties of undefined (reading 'decodeFrame')\n    at ${window.location.origin}/assets/index-Ab12Cd34.js:124:29373\n    at t.<anonymous> (${window.location.origin}/assets/ScenePlayer-BVCijiWf.js:25:35113)`;
    // Execute the installed plugin's CLI-compatible injection, rather than
    // hand-authoring the global chunk map/release property.
    const injected: {
      Error: unknown;
      _posthogReleaseId?: string;
      _posthogChunkIds?: unknown;
    } = {
      Error: class {
        stack = stack;
      },
    };
    runInNewContext(createChunkIdSnippet(chunk, "build-release-vex80"), {
      window: injected,
    });
    Object.assign(globalThis, {
      _posthogReleaseId: injected._posthogReleaseId,
      _posthogChunkIds: injected._posthogChunkIds,
    });
    sdk.addExceptionStep("Decoder started; token=secret", {
      codec: "av1",
      headers: { Authorization: "Bearer secret" },
    });
    const error = new TypeError(
      "Cannot read properties of undefined (reading 'decodeFrame')"
    );
    error.stack = stack;
    const cause = new Error(
      "Decoder queue stalled after 8 frames; token=secret"
    );
    cause.stack = `Error: Decoder queue stalled after 8 frames; token=secret\n    at playbackFailure (${window.location.origin}/assets/ScenePlayer-BVCijiWf.js:25:35113)`;
    Object.assign(error, { cause });
    sdk.captureException(error, {
      operation: "decoder.initialize",
      stage: "configure",
      component: "ScenePlayer",
      acceleration_driver: "mesa-24.2",
      context: {
        codec: "av1",
        pendingFrames: 8,
        ready: false,
        detail:
          "Loading chunk 172 failed: https://user:password@private-host/assets/index.js?token=secret",
        response_body: { title: "Jane Smith" },
        headers: { Authorization: "Bearer secret" },
        media_path: "/mnt/private/movie.mp4",
        transport_failure:
          "Client failed: Cookie: theme=dark; sid=opaque-session-value",
        auth_failure: "Auth failed: password=correct horse battery staple",
        key_failure:
          "TLS failed: -----BEGIN PRIVATE KEY-----\nseeded-key-material\n-----END PRIVATE KEY-----",
        privateKey: "opaque-private-key",
        decoder_failure: "Decoder failed for 旅行.mp4; package av==12.0.0",
        package: "python-tools@2.5.1",
      },
    });
    await vi.waitFor(() => expect(captured).toHaveLength(1));
    const safe = captured[0];
    expect(safe.event).toBe("$exception");
    expect(safe.properties).toMatchObject({
      $release_id: "build-release-vex80",
      acceleration_driver: "mesa-24.2",
      $browser: expect.any(String),
      $browser_version: beforeSend.at(-1)?.properties.$browser_version,
      $sdk_dist_channel: beforeSend.at(-1)?.properties.$sdk_dist_channel,
      $exception_steps: [
        {
          $message: "Decoder started; token=[credential redacted]",
          $timestamp: expect.any(String),
          codec: "av1",
          headers: "[private content redacted]",
        },
      ],
    });
    expect(safe.properties).not.toHaveProperty("$raw_user_agent");
    const evidencePath = (
      globalThis as typeof globalThis & {
        process?: { env: Record<string, string | undefined> };
      }
    ).process?.env.VEX80_SDK_EVIDENCE;
    if (evidencePath)
      writeFileSync(evidencePath, JSON.stringify(safe, null, 2));
    expect(safe.properties).toMatchObject({
      $app_namespace: "vexxx-ui",
      $app_version: "v0.2.1",
      $app_build: "ca01e624a3e8ba001dc8f8081968b2c467aa929f",
      app_revision: "ca01e624a3e8ba001dc8f8081968b2c467aa929f",
      operation: "decoder.initialize",
      stage: "configure",
      component: "ScenePlayer",
      context: {
        codec: "av1",
        pendingFrames: 8,
        ready: false,
        detail: "Loading chunk 172 failed: [URL redacted]",
        response_body: "[private content redacted]",
        headers: "[private content redacted]",
        media_path: "[private content redacted]",
        transport_failure: "Client failed: Cookie: [credential redacted]",
        auth_failure: "Auth failed: password=[credential redacted]",
        key_failure: "TLS failed: [private key redacted]",
        privateKey: "[private content redacted]",
        decoder_failure:
          "Decoder failed for [media redacted]; package av==12.0.0",
        package: "python-tools@2.5.1",
      },
    });
    expect(safe.properties.$exception_level).toBe("error");
    expect(safe.properties.$exception_list).toHaveLength(2);
    expect(safe.properties.$exception_list[0].value).toBe(
      "Cannot read properties of undefined (reading 'decodeFrame')"
    );
    expect(safe.properties.$exception_list[0].mechanism).toMatchObject({
      type: "generic",
      handled: true,
      synthetic: false,
      exception_id: 0,
    });
    expect(safe.properties.$exception_list[1]).toMatchObject({
      type: "Error",
      value:
        "Decoder queue stalled after 8 frames; token=[credential redacted]",
      mechanism: {
        type: "chained",
        source: "cause",
        synthetic: false,
        exception_id: 1,
        parent_id: 0,
      },
    });
    expect(safe.properties.$exception_list[1].mechanism).not.toHaveProperty(
      "handled"
    );
    // The pinned parser keeps call-site order (outer frame first); only the
    // bundle identified by the injected chunk map receives its chunk ID.
    expect(safe.properties.$exception_list[0].stacktrace.frames).toEqual([
      {
        filename: "/assets/ScenePlayer-BVCijiWf.js",
        platform: "web:javascript",
        in_app: true,
        lineno: 25,
        colno: 35113,
        function: "t.<anonymous>",
      },
      {
        filename: "/assets/index-Ab12Cd34.js",
        platform: "web:javascript",
        in_app: true,
        lineno: 124,
        colno: 29373,
        function: "?",
        chunk_id: chunk,
      },
    ]);
    expect(JSON.stringify(safe)).not.toMatch(
      /private-file|private-host|movie.mp4|Jane Smith|token=secret|Bearer secret|user:password|opaque-session-value|correct horse|seeded-key-material|opaque-private-key|旅行/
    );
    expect(fetch).not.toHaveBeenCalled();
    expect(xhr).not.toHaveBeenCalled();
  });
  it("does not deliver or imply acceptance for missing and opted-out SDK clients", () => {
    const missing = new PostHog();
    const missingTransport = vi.spyOn(missing, "_send_retriable_request");
    const warning = vi.spyOn(console, "warn").mockImplementation(() => {});
    expect(
      missing.captureException(new Error("Decoder unavailable"))
    ).toBeUndefined();
    expect(missingTransport).not.toHaveBeenCalled();
    warning.mockRestore();
    const optedOut = new PostHog();
    const disabledTransport = vi.spyOn(optedOut, "_send_retriable_request");
    optedOut.init("test-opted-out-token", {
      ...telemetryConfig,
      api_host: "https://example.invalid",
      persistence: "memory",
      capture_pageview: false,
      capture_exceptions: false,
      opt_out_capturing_by_default: true,
      opt_out_persistence_by_default: true,
      bootstrap: { distinctID: "00000000-0000-4000-8000-000000000000" },
    });
    expect(
      optedOut.captureException(new Error("Decoder unavailable"))
    ).toBeUndefined();
    expect(disabledTransport).not.toHaveBeenCalled();
  });
  it("protects parsed causes and top-level opaque content through actual SDK transport", async () => {
    const fetch = vi
      .spyOn(globalThis, "fetch")
      .mockRejectedValue(new Error("network forbidden"));
    const xhr = vi
      .spyOn(XMLHttpRequest.prototype, "send")
      .mockImplementation(() => {
        throw new Error("network forbidden");
      });
    const captured: CaptureResult[] = [];
    const sdk = new PostHog();
    vi.spyOn(sdk, "_send_retriable_request").mockImplementation((request) => {
      captured.push(request.data as CaptureResult);
    });
    sdk.init("test-public-review-token", {
      ...telemetryConfig,
      api_host: "https://example.invalid",
      persistence: "memory",
      bootstrap: { distinctID: "00000000-0000-4000-8000-000000000000" },
      capture_pageview: false,
      capture_exceptions: false,
      request_batching: false,
    });
    captured.length = 0;
    const wideContext = Object.fromEntries(
      Array.from({ length: 10 }, (_, index) => [
        `decoder${index}`,
        Object.fromEntries(
          Array.from({ length: 10000 }, (_value, metric) => [
            `metric${metric}`,
            metric,
          ])
        ),
      ])
    );
    let chain: Error | undefined;
    for (let index = 49; index >= 0; index -= 1) {
      const next = new Error(`Decoder cause ${index}`);
      next.stack = `Error: Decoder cause ${index}\n    at decodeFrame (${
        window.location.origin
      }/assets/index-Ab12Cd34.js:${index + 10}:12)`;
      if (chain) Object.assign(next, { cause: chain });
      chain = next;
    }
    sdk.captureException(chain, {
      operation: "decoder.initialize",
      acceleration: { codec: "av1", frames: 8 },
      context: wideContext,
      input: "Jane Smith",
      content: ["Unlabelled private media name"],
      variables: "Jane Smith",
      args: ["Jane Smith"],
      data: "Jane Smith",
    });
    await vi.waitFor(() => expect(captured).toHaveLength(1));
    const safe = captured[0];
    expect(safe.properties.$exception_list).toHaveLength(50);
    expect(
      safe.properties.$exception_list.map(
        (item: { value: string }) => item.value
      )
    ).toEqual(
      Array.from({ length: 50 }, (_, index) => `Decoder cause ${index}`)
    );
    expect(safe.properties.$exception_list[49].mechanism).toMatchObject({
      exception_id: 49,
      parent_id: 48,
      source: "cause",
    });
    expect(
      safe.properties.$exception_list[49].stacktrace.frames[0]
    ).toMatchObject({
      filename: "/assets/index-Ab12Cd34.js",
      lineno: 59,
      function: "decodeFrame",
    });
    for (const key of ["input", "content", "variables", "args", "data"])
      expect(safe.properties[key]).toBe("[private content redacted]");
    expect(safe.properties.acceleration).toEqual({ codec: "av1", frames: 8 });
    expect(JSON.stringify(safe)).not.toMatch(
      /Jane Smith|Unlabelled private media name/
    );
    expect(fetch).not.toHaveBeenCalled();
    expect(xhr).not.toHaveBeenCalled();
    const evidencePath = (
      globalThis as typeof globalThis & {
        process?: { env: Record<string, string | undefined> };
      }
    ).process?.env.VEX80_R2_SDK_EVIDENCE;
    if (evidencePath)
      writeFileSync(evidencePath, JSON.stringify(safe, null, 2));
  });
});
