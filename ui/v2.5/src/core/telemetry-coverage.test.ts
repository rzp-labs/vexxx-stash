import { afterEach, describe, expect, it, vi } from "vitest";
import {
  sanitizeTelemetry,
  telemetryConfig,
  telemetryDeliveryHealth,
} from "./telemetry";
import {
  diagnosticAccounting,
  diagnosticContext,
  diagnosticMessage,
  diagnosticLimits,
} from "./telemetry-redaction";
const event = (properties: Record<string, unknown>, name = "$exception") => ({
  uuid: "00000000-0000-4000-8000-000000000000",
  event: name,
  properties,
});
afterEach(() => {
  delete (globalThis as typeof globalThis & { _posthogReleaseId?: unknown })
    ._posthogReleaseId;
  vi.restoreAllMocks();
});
describe("comprehensive diagnostic preservation", () => {
  it("preserves injected release identity, environment, SDK metadata, navigation, breadcrumbs and novel diagnostics", () => {
    Object.assign(globalThis, {
      _posthogReleaseId: "release-build-2026.10.09",
    });
    const safe = sanitizeTelemetry(
      event({
        $release_id: "untrusted-caller",
        $os: "Linux",
        $os_version: "6.8",
        $browser: "Firefox",
        $browser_version: 130,
        $browser_language: "en-GB",
        $device_type: "Desktop",
        $screen_width: 1920,
        $viewport_height: 947,
        $raw_user_agent: "Private device string",
        $timezone: "Private city",
        $current_url: "https://private/media.mp4?token=secret",
        $navigation_id: "11111111-2222-4333-8444-555555555555",
        $config_defaults: "2026-01-30",
        $sdk_dist_channel: "no-external",
        $sdk_debug_retry_queue_size: 2,
        $exception_steps: [
          {
            $message: "Decoder started; token=secret",
            $timestamp: 1234,
            codec: "av1",
          },
        ],
        acceleration: {
          driver: "mesa",
          data: { dimensions: [1920, 1080], retry: false },
        },
        $exception_list: [
          {
            type: "DecoderStateError",
            value: "Frame 64 unavailable",
            module: "decoder",
            thread_id: 1,
          },
        ],
      })
    );
    expect(safe?.properties).toMatchObject({
      $release_id: "release-build-2026.10.09",
      $os: "Linux",
      $os_version: "6.8",
      $browser: "Firefox",
      $browser_version: 130,
      $screen_width: 1900,
      $viewport_height: 900,
      $sdk_debug_retry_queue_size: 2,
      $navigation_id: "11111111-2222-4333-8444-555555555555",
      $exception_steps: [
        {
          $message: "Decoder started; token=[credential redacted]",
          $timestamp: 1234,
          codec: "av1",
        },
      ],
      acceleration: {
        driver: "mesa",
        data: { dimensions: [1920, 1080], retry: false },
      },
      $exception_list: [{ module: "decoder", thread_id: 1 }],
    });
    expect(JSON.stringify(safe)).not.toMatch(
      /Private device|Private city|untrusted-caller|media.mp4|token=secret/
    );
  });
  it("retains all fifty SDK causes and reports manual envelope limits and omissions", () => {
    const safe = sanitizeTelemetry(
      event({
        $exception_list: [
          null,
          {},
          ...Array.from({ length: 60 }, (_, n) => ({
            type: "Error",
            value: `cause ${n}`,
            mechanism: {
              exception_id: n,
              ...(n ? { parent_id: n - 1, source: "cause" } : {}),
            },
          })),
        ],
      })
    );
    expect(safe?.properties.$exception_list).toHaveLength(50);
    expect(safe?.properties.$exception_list[49].mechanism).toMatchObject({
      exception_id: 49,
      parent_id: 48,
    });
    expect(safe?.properties.telemetry_diagnostics).toMatchObject({
      exceptions_supplied: 62,
      exceptions_retained: 50,
      truncated: 1,
    });
  });
  it("avoids getters and reports cycles, unsupported values and truncation", () => {
    const accounting = diagnosticAccounting();
    const getter = vi.fn(() => {
      throw new Error("getter must not execute");
    });
    const value: Record<string, unknown> = {
      useful: "Decoder unavailable",
      overflow: Array(101).fill("retry"),
      callback: () => {},
      password: "opaque",
      $token: "opaque",
    };
    Object.defineProperty(value, "dynamic", { enumerable: true, get: getter });
    value.cycle = value;
    const result = diagnosticContext(value, accounting);
    expect(getter).not.toHaveBeenCalled();
    expect(result).toMatchObject({
      useful: "Decoder unavailable",
      password: "[private content redacted]",
      $token: "[private content redacted]",
      cycle: "[circular context omitted]",
    });
    expect(accounting).toMatchObject({
      redacted: 2,
      truncated: 1,
      omitted: 1,
      cycles: 1,
      accessors: 1,
    });
  });
  it("retains structured technical data while hiding opaque request input and credentials", () => {
    expect(
      diagnosticContext({
        data: { codec: "av1", pending: 8 },
        input: "Jane Smith",
        args: ["private media"],
        token: "secret",
        variables: { password: "secret", retry: 2 },
      })
    ).toEqual({
      data: { codec: "av1", pending: 8 },
      input: "[private content redacted]",
      args: "[private content redacted]",
      token: "[private content redacted]",
      variables: { password: "[private content redacted]", retry: 2 },
    });
  });
  it("keeps rate-limit warning counts with private page details redacted", () => {
    const safe = sanitizeTelemetry(
      event(
        {
          $$client_ingestion_warning_message:
            "posthog-js client rate limited: 12 event(s) dropped since the last warning, triggered on https://private.example/media.mp4. Config is set to 10 events per second and 100 events burst limit.",
        },
        "$$client_ingestion_warning"
      )
    );
    expect(safe?.properties.$$client_ingestion_warning_message).toContain(
      "12 event(s) dropped"
    );
    expect(safe?.properties.$$client_ingestion_warning_message).toContain(
      "100 events burst limit"
    );
    expect(JSON.stringify(safe)).not.toContain("private.example");
  });
  it("counts SDK HTTP failure attempts locally without recursive capture", () => {
    const before = telemetryDeliveryHealth();
    telemetryConfig.on_request_error?.({
      statusCode: 429,
      text: "Private response",
    });
    telemetryConfig.on_request_error?.({
      statusCode: 503,
      text: "Private response",
    });
    expect(telemetryDeliveryHealth()).toMatchObject({
      requestFailures: before.requestFailures + 2,
      lastHTTPStatus: 503,
    });
    const safe = sanitizeTelemetry(event({}));
    expect(safe?.properties.telemetry_delivery.requestFailures).toBe(
      before.requestFailures + 2
    );
    expect(JSON.stringify(safe)).not.toContain("Private response");
  });
  it("bounds oversized strings without leaking a cut secret prefix", () => {
    const accounting = diagnosticAccounting();
    const result = diagnosticMessage(
      `TLS failed: -----BEGIN PRIVATE KEY-----\n${"private-key-material".repeat(
        100000
      )}`,
      accounting
    );
    expect(result).toBe("TLS failed: [private key redacted] [truncated]");
    expect(accounting.truncated).toBe(1);
  });
  it("bounds descriptor work and retains safe fields beyond a malformed-key prefix", () => {
    const value = Object.fromEntries([
      ...Array.from({ length: 40 }, (_, n) => [`private key ${n}`, "invalid"]),
      ["codec", "av1"],
      ...Array.from({ length: 20000 }, (_, n) => [`metric${n}`, n]),
    ]);
    const accounting = diagnosticAccounting();
    const safe = diagnosticContext(value, accounting) as Record<
      string,
      unknown
    >;
    expect(safe.codec).toBe("av1");
    expect(Object.keys(safe)).toHaveLength(100);
    expect(accounting.inspected).toBeLessThanOrEqual(10001);
    expect(accounting.truncated).toBeGreaterThan(0);
    expect(accounting.omitted).toBe(40);
  });
  it("does not evaluate an accessor inside an opaque-content array", () => {
    const getter = vi.fn(() => {
      throw new Error("must not execute");
    });
    const items = [0];
    Object.defineProperty(items, "0", { enumerable: true, get: getter });
    const accounting = diagnosticAccounting();
    expect(diagnosticContext({ data: items }, accounting)).toEqual({
      data: [null],
    });
    expect(getter).not.toHaveBeenCalled();
    expect(accounting.accessors).toBe(1);
  });
  it("bounds context text separately so oversized output cannot consume cause messages", () => {
    const safe = sanitizeTelemetry(
      event({
        context: {
          diagnostics: Array.from({ length: 100 }, (_, n) =>
            n % 2 ? "x.".repeat(4096) : "x".repeat(8192)
          ),
        },
        $exception_list: Array.from({ length: 50 }, (_, n) => ({
          type: "Error",
          value: `Diagnostic cause ${n}`,
          mechanism: { exception_id: n },
        })),
      })
    );
    expect(JSON.stringify(safe?.properties.context).length).toBeLessThan(
      140000
    );
    expect(safe?.properties.telemetry_diagnostics.truncated).toBeGreaterThan(0);
    expect(safe?.properties.$exception_list[49].value).toBe(
      "Diagnostic cause 49"
    );
  });
});

describe("R1a review regressions", () => {
  it("keeps fifty causes and frames when wide context exhausts its inspection budget", () => {
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
    const chunk = "11111111-2222-4333-8444-555555555555";
    const causes = Array.from({ length: 50 }, (_, index) => ({
      type: index ? "DecoderStateError" : "TypeError",
      value: index
        ? `Decoder cause ${index}`
        : "Cannot read properties of undefined (reading 'decodeFrame')",
      mechanism: {
        exception_id: index,
        ...(index
          ? { parent_id: index - 1, type: "chained", source: "cause" }
          : { type: "generic" }),
      },
      stacktrace: {
        frames: [
          {
            filename: "/assets/index-Ab12Cd34.js",
            lineno: index + 10,
            colno: 12,
            function: "decodeFrame",
            chunk_id: chunk,
          },
        ],
      },
    }));
    const safe = sanitizeTelemetry(
      event({ context: wideContext, $exception_list: causes })
    );
    const retained = safe?.properties.$exception_list;
    expect(retained).toHaveLength(50);
    expect(retained.map((item: { value: string }) => item.value)).toEqual(
      causes.map((item) => item.value)
    );
    expect(
      retained.map(
        (item: { mechanism: { exception_id: number } }) =>
          item.mechanism.exception_id
      )
    ).toEqual(Array.from({ length: 50 }, (_, index) => index));
    expect(retained[49].mechanism).toMatchObject({
      parent_id: 48,
      type: "chained",
      source: "cause",
    });
    expect(
      retained.map(
        (item: {
          stacktrace: { frames: { lineno: number; chunk_id: string }[] };
        }) => item.stacktrace.frames[0]
      )
    ).toEqual(
      causes.map((item) => ({
        ...item.stacktrace.frames[0],
        platform: "web:javascript",
        in_app: true,
      }))
    );
    expect(safe?.properties.context.decoder0.metric0).toBe(0);
    expect(safe?.properties.telemetry_diagnostics.truncated).toBeGreaterThan(0);
    expect(safe?.properties.telemetry_diagnostics.inspection.context).toBe(
      diagnosticLimits.inspectedProperties
    );
    expect(
      safe?.properties.telemetry_diagnostics.inspection.exceptions
    ).toBeGreaterThan(0);
    expect(
      safe?.properties.telemetry_diagnostics.inspection.exceptions
    ).toBeLessThanOrEqual(diagnosticLimits.inspectedProperties);
    expect(
      safe?.properties.telemetry_diagnostics.inspected
    ).toBeLessThanOrEqual(2 * diagnosticLimits.inspectedProperties);
  });

  it.each(
    ["input", "content", "variables", "args", "data"].flatMap((key) => [
      [key, "string"],
      [key, "string-array"],
    ])
  )("masks top-level %s %s exactly like nested opaque content", (key, kind) => {
    const privateValue =
      kind === "string"
        ? "Jane Smith"
        : ["Jane Smith", "Unlabelled private media name"];
    const safe = sanitizeTelemetry(
      event({
        [key]: privateValue,
        context: { [key]: privateValue },
        operation: "decoder.initialize",
        detail: "Unfamiliar decoder queue failure after 8 frames",
        $exception_list: [
          {
            type: "DecoderStateError",
            value: "Decoder queue stalled after 8 frames",
          },
        ],
      })
    );
    expect(safe?.properties[key]).toBe("[private content redacted]");
    expect(safe?.properties.context[key]).toBe("[private content redacted]");
    expect(safe?.properties.telemetry_diagnostics.redacted).toBe(2);
    expect(safe?.properties).toMatchObject({
      operation: "decoder.initialize",
      detail: "Unfamiliar decoder queue failure after 8 frames",
    });
    expect(safe?.properties.$exception_list[0].value).toBe(
      "Decoder queue stalled after 8 frames"
    );
    expect(JSON.stringify(safe)).not.toMatch(
      /Jane Smith|Unlabelled private media name/
    );
  });

  it.each(["input", "content", "variables", "args", "data"])(
    "retains top-level %s structured technical context with nested credential protection",
    (key) => {
      const metadata = {
        codec: "av1",
        frames: 8,
        retry: false,
        token: "opaque credential",
      };
      const safe = sanitizeTelemetry(
        event({ [key]: metadata, context: { [key]: metadata } })
      );
      const expected = {
        codec: "av1",
        frames: 8,
        retry: false,
        token: "[private content redacted]",
      };
      expect(safe?.properties[key]).toEqual(expected);
      expect(safe?.properties.context[key]).toEqual(expected);
      expect(JSON.stringify(safe)).not.toContain("opaque credential");
    }
  );
});
