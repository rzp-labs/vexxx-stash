import { describe, expect, it } from "vitest";
import { validateGenerationSettings } from "./generationSettingsValidation";

const software = {
  generationMarkerBackend: "software",
  generationSpriteBackend: "software",
  generationDevice: "/dev/dri/renderD128",
  generationBudgetEnabled: false,
  generationMaxProcesses: 0,
  generationMaxGPUProcesses: 0,
  generationThreads: 0,
};

describe("generation settings proposals", () => {
  it("retains software defaults and runtime automatic limits", () => {
    expect(validateGenerationSettings(software)).toBeUndefined();
    expect(
      validateGenerationSettings({
        ...software,
        generationMarkerBackend: "vaapi",
        generationSpriteBackend: "qsv",
      })
    ).toBeUndefined();
  });
  it.each([-1, 2 ** 31, 1.5, NaN])(
    "rejects invalid limits %s",
    (generationThreads) => {
      expect(
        validateGenerationSettings({ ...software, generationThreads })
      ).toBe("config.general.generation.invalid_limits");
    }
  );
  it("validates explicit coupled limits and preserves Auto requests", () => {
    expect(
      validateGenerationSettings({ ...software, generationMaxGPUProcesses: 2 })
    ).toBeUndefined();
    expect(
      validateGenerationSettings({
        ...software,
        generationMaxProcesses: 1,
        generationMaxGPUProcesses: 2,
      })
    ).toBe("config.general.generation.invalid_gpu_limit");
    expect(
      validateGenerationSettings({
        ...software,
        generationMaxProcesses: 2,
        generationMaxGPUProcesses: 2,
      })
    ).toBeUndefined();
  });
  it("rejects unsupported backends and devices before saving", () => {
    expect(
      validateGenerationSettings({
        ...software,
        generationMarkerBackend: "native",
      })
    ).toBe("config.general.generation.invalid_backend");
    expect(
      validateGenerationSettings({
        ...software,
        generationDevice: "renderD128",
      })
    ).toBe("config.general.generation.invalid_device");
  });
});

it("keeps scene previews independent and rejects unvalidated QSV", () => {
  expect(
    validateGenerationSettings({
      ...software,
      generationPreviewBackend: "vaapi",
    })
  ).toBeUndefined();
  expect(
    validateGenerationSettings({ ...software, generationPreviewBackend: "qsv" })
  ).toBe("config.general.generation.invalid_backend");
});

it("accepts explicit values above64 and preserves automatic coupled requests", () => {
  expect(
    validateGenerationSettings({
      ...software,
      generationMaxProcesses: 256,
      generationMaxGPUProcesses: 128,
      generationThreads: 96,
    })
  ).toBeUndefined();
  expect(
    validateGenerationSettings({
      ...software,
      generationMaxProcesses: 1,
      generationMaxGPUProcesses: 0,
    })
  ).toBeUndefined();
});
