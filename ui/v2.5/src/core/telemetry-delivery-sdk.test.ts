import { afterAll, afterEach, describe, expect, it, vi } from "vitest";
const transport = vi.hoisted(() => {
  const fetch = vi.fn();
  // Install before the bundled SDK captures its global fetch reference.
  vi.stubGlobal("fetch", fetch);
  return fetch;
});
import { PostHog, CaptureResult } from "posthog-js/no-external";
import { telemetryConfig, telemetryDeliveryHealth } from "./telemetry";
afterAll(() => vi.unstubAllGlobals());
afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});
function sdk() {
  const client = new PostHog();
  client.init("test-public-token", {
    ...telemetryConfig,
    api_host: "https://example.invalid",
    persistence: "memory",
    request_batching: false,
    capture_pageview: false,
    capture_exceptions: false,
    bootstrap: { distinctID: "00000000-0000-4000-8000-000000000000" },
  });
  return client;
}
describe("SDK delivery observations", () => {
  it("observes an actual failed SDK HTTP attempt without recording response data", async () => {
    transport.mockResolvedValue(
      new Response("private response Jane Smith", { status: 503 })
    );
    const xhr = vi
      .spyOn(XMLHttpRequest.prototype, "send")
      .mockImplementation(() => {
        throw new Error("network forbidden");
      });
    const client = sdk();
    const before = telemetryDeliveryHealth();
    // Exercise SDK dispatch/callback without starting an uncontrolled retry timer.
    client._send_request({
      url: "https://example.invalid/e/",
      method: "POST",
      data: { event: "$exception", properties: {} },
      transport: "fetch",
    });
    await vi.waitFor(() =>
      expect(telemetryDeliveryHealth().requestFailures).toBe(
        before.requestFailures + 1
      )
    );
    expect(telemetryDeliveryHealth().lastHTTPStatus).toBe(503);
    expect(JSON.stringify(telemetryDeliveryHealth())).not.toMatch(
      /Jane Smith|private response/
    );
    expect(transport).toHaveBeenCalledTimes(1);
    expect(xhr).not.toHaveBeenCalled();
  });
  it("retains the SDK's real rate-limit warning while its flood guard drops events", () => {
    vi.useFakeTimers();
    const consoleError = vi
      .spyOn(console, "error")
      .mockImplementation(() => {});
    const client = sdk();
    const accepted: CaptureResult[] = [];
    vi.spyOn(client, "_send_retriable_request").mockImplementation(
      (request) => {
        accepted.push(request.data as CaptureResult);
      }
    );
    const available = Math.floor(
      client.rateLimiter.clientRateLimitContext(true).remainingTokens
    );
    expect(available).toBeGreaterThan(0);
    expect(available).toBeLessThanOrEqual(100);
    let rejected = 0;
    for (let n = 0; n < 120; n += 1)
      if (
        !client.capture("$exception", {
          $exception_list: [{ type: "Error", value: `Decoder state ${n}` }],
        })
      )
        rejected += 1;
    expect(rejected).toBe(120 - available);
    expect(consoleError).toHaveBeenCalledTimes(120 - available);
    expect(
      accepted.filter((event) => event.event === "$exception")
    ).toHaveLength(available);
    const warnings = accepted.filter(
      (event) => event.event === "$$client_ingestion_warning"
    );
    expect(warnings).toHaveLength(1);
    expect(warnings[0].properties.$$client_ingestion_warning_message).toContain(
      "1 event(s) dropped since the last warning"
    );
    expect(warnings[0].properties.$$client_ingestion_warning_message).toContain(
      "100 events burst limit"
    );
    expect(
      warnings[0].properties.$$client_ingestion_warning_message
    ).not.toContain(window.location.origin);
  });
});
