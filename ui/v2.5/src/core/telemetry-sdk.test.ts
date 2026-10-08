import { afterEach, describe, expect, it, vi } from "vitest";
import { PostHog, CaptureResult } from "posthog-js/no-external";
import { telemetryConfig } from "./telemetry";

// Exercise the installed parser and production before_send, then intercept the
// request handed to SDK transport. No telemetry leaves this offline test.
describe("pinned PostHog error parser", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    delete (globalThis as typeof globalThis & { _posthogChunkIds?: unknown })
      ._posthogChunkIds;
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
    vi.spyOn(sdk, "_send_retriable_request").mockImplementation((request) => {
      captured.push(request.data as CaptureResult);
    });
    sdk.init("test-public-project-token", {
      ...telemetryConfig,
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
    Object.assign(globalThis, { _posthogChunkIds: { [stack]: chunk } });
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
});
