import { describe, expect, it, vi } from "vitest";
import { sanitizeTelemetry } from "./telemetry";

vi.mock("posthog-js/no-external", () => ({ default: { init: vi.fn() } }));
vi.mock("posthog-js/dist/exception-autocapture", () => ({}));

const bundle = (lineno: number) => ({
  filename: `${window.location.origin}/assets/index-Ab12Cd34.js`,
  lineno,
});
const plugin = (lineno: number) => ({
  filename: `${window.location.origin}/plugin/PrivatePlugin/javascript?token=private-token`,
  function: "PrivateFunction",
  lineno,
  colno: 10,
});
const sanitize = (frames: unknown[]) =>
  sanitizeTelemetry({
    uuid: "00000000-0000-4000-8000-000000000000",
    event: "$exception",
    properties: {
      token: "public-ingestion-token",
      $exception_list: [{ type: "TypeError", stacktrace: { frames } }],
    },
  })?.properties;

describe("independent diagnostic frame limits for manual envelopes", () => {
  it("retains a plugin frame after fifty accepted bundle frames", () => {
    const safe = sanitize([
      ...Array.from({ length: 50 }, (_, i) => bundle(i + 1)),
      plugin(9),
    ]);
    expect(safe?.$exception_list[0].stacktrace.frames).toHaveLength(50);
    expect(safe?.plugin_script_frames).toEqual([
      {
        exception_index: 0,
        source: "plugin.javascript",
        frames: [{ lineno: 9, colno: 10 }],
      },
    ]);
    expect(JSON.stringify(safe)).not.toMatch(/Private|private-token/);
  });

  it.each([false, true])(
    "retains the first fifty frames of each category in source order (plugins first: %s)",
    (pluginsFirst) => {
      const bundles = Array.from({ length: 60 }, (_, i) => bundle(i + 1));
      const plugins = Array.from({ length: 60 }, (_, i) => plugin(i + 101));
      const safe = sanitize(
        pluginsFirst ? [...plugins, ...bundles] : [...bundles, ...plugins]
      );
      expect(
        safe?.$exception_list[0].stacktrace.frames.map(
          (frame: { lineno: number }) => frame.lineno
        )
      ).toEqual(Array.from({ length: 50 }, (_, i) => i + 1));
      expect(safe?.plugin_script_frames[0].frames).toEqual(
        Array.from({ length: 50 }, (_, i) => ({ lineno: i + 101, colno: 10 }))
      );
      expect(safe?.telemetry_diagnostics.truncated).toBeGreaterThanOrEqual(20);
      expect(JSON.stringify(safe)).not.toMatch(/Private|private-token/);
    }
  );

  it("inspects only the bounded window and skips accessors after the bundle quota", () => {
    const frames: unknown[] = Array.from({ length: 10001 }, (_, i) =>
      bundle(i)
    );
    const getter = vi.fn(() => plugin(999));
    Object.defineProperty(frames, "50", { get: getter });
    frames[9999] = plugin(9999);
    Object.defineProperty(frames, "10000", { get: getter });
    const safe = sanitize(frames);
    expect(getter).not.toHaveBeenCalled();
    expect(safe?.$exception_list[0].stacktrace.frames).toHaveLength(50);
    expect(safe?.plugin_script_frames[0].frames).toEqual([
      { lineno: 9999, colno: 10 },
    ]);
    expect(safe?.telemetry_diagnostics.accessors).toBe(1);
    expect(safe?.telemetry_diagnostics.truncated).toBeGreaterThan(0);
  });
});
