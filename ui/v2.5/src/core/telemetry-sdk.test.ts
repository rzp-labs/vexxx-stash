import { afterEach, describe, expect, it, vi } from "vitest";
import { PostHog, CaptureResult } from "posthog-js/no-external";
import { sanitizeTelemetry, telemetryConfig } from "./telemetry";

// Exercise the pinned real parser/config, but drop every event at the final
// transport boundary. Any attempted network request fails locally in the test.
describe("pinned PostHog error parser", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllEnvs();
  });
  it("retains symbolication metadata from an actual SDK exception without sending", async () => {
    vi.stubEnv("VITE_APP_STASH_VERSION", "v0.1.0");
    vi.stubEnv("VITE_APP_GITHASH", "890c5f1");
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
    sdk.init("test-public-project-token", {
      ...telemetryConfig,
      api_host: "https://example.invalid",
      persistence: "memory",
      bootstrap: { distinctID: "00000000-0000-4000-8000-000000000000" },
      capture_pageview: false,
      capture_exceptions: false,
      before_send: (event) => {
        const safe = sanitizeTelemetry(event);
        if (safe) captured.push(safe);
        return null;
      },
    });
    sdk.identify("42", { user_role: "ADMIN" });
    expect(captured).toHaveLength(1);
    expect(captured[0].properties).toMatchObject({
      distinct_id: "42",
      $user_id: "42",
      $set: { user_role: "ADMIN" },
      $app_namespace: "vexxx-ui",
      $app_version: "v0.1.0",
      $app_build: "890c5f1",
    });
    captured.length = 0;
    const chunk = "11111111-2222-4333-8444-555555555555";
    const stack = `TypeError: private-file.mp4 secret\n    at ${window.location.origin}/assets/index-Ab12Cd34.js:124:29373\n    at t.<anonymous> (${window.location.origin}/assets/ScenePlayer-BVCijiWf.js:25:35113)`;
    Object.assign(globalThis, { _posthogChunkIds: { [stack]: chunk } });
    const error = new TypeError("private-file.mp4 secret");
    error.stack = stack;
    const cause = new RangeError("Maximum call stack size exceeded");
    cause.stack = `RangeError: Maximum call stack size exceeded\n    at playbackFailure (${window.location.origin}/assets/ScenePlayer-BVCijiWf.js:25:35113)`;
    Object.assign(error, { cause });
    sdk.captureException(error);
    await vi.waitFor(() => expect(captured).toHaveLength(1));
    const safe = captured[0];
    expect(safe.event).toBe("$exception");
    expect(safe.properties).toMatchObject({
      $app_namespace: "vexxx-ui",
      $app_version: "v0.1.0",
      $app_build: "890c5f1",
      app_revision: "890c5f1",
    });
    expect(safe.properties.$exception_level).toBe("error");
    expect(safe.properties.$exception_list).toHaveLength(2);
    expect(safe.properties.$exception_list[0].mechanism).toMatchObject({
      type: "generic",
      handled: true,
      synthetic: false,
      exception_id: 0,
    });
    expect(safe.properties.$exception_list[1]).toMatchObject({
      type: "RangeError",
      value: "Maximum call stack size exceeded",
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
      /private-file|private-host|secret/
    );
    expect(fetch).not.toHaveBeenCalled();
    expect(xhr).not.toHaveBeenCalled();
    delete (globalThis as typeof globalThis & { _posthogChunkIds?: unknown })
      ._posthogChunkIds;
  });
});
