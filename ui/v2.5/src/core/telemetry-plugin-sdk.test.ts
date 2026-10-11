import { afterEach, describe, expect, it, vi } from "vitest";
import { PostHog, CaptureResult } from "posthog-js/no-external";
import { telemetryConfig } from "./telemetry";

afterEach(() => vi.restoreAllMocks());
describe("plugin script attribution through offline SDK transport", () => {
  it("retains plugin source coordinates while removing private IDs, URLs and function names", async () => {
    const fetch = vi
      .spyOn(globalThis, "fetch")
      .mockRejectedValue(new Error("network forbidden"));
    const xhr = vi
      .spyOn(XMLHttpRequest.prototype, "send")
      .mockImplementation(() => {
        throw new Error("network forbidden");
      });
    const events: CaptureResult[] = [];
    const sdk = new PostHog();
    vi.spyOn(sdk, "_send_retriable_request").mockImplementation((request) => {
      events.push(request.data as CaptureResult);
    });
    sdk.init("test-offline-plugin-token", {
      ...telemetryConfig,
      api_host: "https://example.invalid",
      persistence: "memory",
      bootstrap: { distinctID: "00000000-0000-4000-8000-000000000000" },
      capture_pageview: false,
      capture_exceptions: false,
      request_batching: false,
    });
    const failure = new TypeError(
      "Cannot destructure property 'Button' of 'api.libraries.Bootstrap' as it is undefined."
    );
    failure.stack = `${failure.name}: ${failure.message}\n    at JaneSmithPrivateFunction (${window.location.origin}/plugin/JaneSmithPrivatePlugin/javascript?token=private-token:9:10)\n    at JaneSmithForeignFunction (https://private.example/plugin/JaneSmithPrivatePlugin/javascript:19:20)\n    at render (${window.location.origin}/assets/index-Ab12Cd34.js:29:30)`;
    sdk.captureException(failure, {
      plugin_script_frames: [{ source: "untrusted-caller" }],
    });
    await vi.waitFor(() => expect(events).toHaveLength(1));
    expect(events[0].properties.plugin_script_frames).toEqual([
      {
        exception_index: 0,
        source: "plugin.javascript",
        frames: [{ lineno: 9, colno: 10 }],
      },
    ]);
    expect(
      events[0].properties.$exception_list[0].stacktrace.frames
    ).toMatchObject([
      { filename: "/assets/index-Ab12Cd34.js", lineno: 29, colno: 30 },
    ]);
    expect(JSON.stringify(events)).not.toMatch(
      /JaneSmith|private-token|private.example|untrusted-caller/
    );
    const noFrames = new TypeError("Bootstrap API is unavailable");
    noFrames.stack = `${noFrames.name}: ${noFrames.message}`;
    sdk.captureException(noFrames);
    await vi.waitFor(() => expect(events).toHaveLength(2));
    expect(events[1].properties).not.toHaveProperty("plugin_script_frames");
    expect(events[1].properties.$exception_list[0].stacktrace.frames).toEqual(
      []
    );
    expect(fetch).not.toHaveBeenCalled();
    expect(xhr).not.toHaveBeenCalled();
  });
});
