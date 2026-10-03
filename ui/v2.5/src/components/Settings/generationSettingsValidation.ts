import type { ConfigGeneralInput } from "src/core/generated-graphql";

export function validateGenerationSettings(input: ConfigGeneralInput) {
  if (
    !["software", "qsv", "vaapi"].includes(
      input.generationMarkerBackend ?? ""
    ) ||
    !["software", "qsv", "vaapi"].includes(input.generationSpriteBackend ?? "")
  ) {
    return "config.general.generation.invalid_backend";
  }
  if (!/^\/dev\/dri\/renderD[0-9]+$/.test(input.generationDevice ?? "")) {
    return "config.general.generation.invalid_device";
  }
  const limits = [
    input.generationMaxProcesses,
    input.generationMaxGPUProcesses,
    input.generationThreads,
  ];
  if (
    limits.some((v) => v == null || !Number.isInteger(v) || v < 0 || v > 64)
  ) {
    return "config.general.generation.invalid_limits";
  }
  if (
    (input.generationMaxGPUProcesses || 1) > (input.generationMaxProcesses || 1)
  ) {
    return "config.general.generation.invalid_gpu_limit";
  }
  return undefined;
}

// A failed saved-value parse can return partial defaults or invalid strings.
// Preserve readable values in the proposal; coupled limits require explicit correction.
export function normalizeGenerationSettings(
  input: ConfigGeneralInput
): ConfigGeneralInput {
  const backend = (value: string | null | undefined) =>
    ["software", "qsv", "vaapi"].includes(value ?? "") ? value! : "software";
  const limit = (value: number | null | undefined) =>
    value != null && Number.isInteger(value) && value >= 0 && value <= 64
      ? value
      : 0;
  const maxProcesses = limit(input.generationMaxProcesses);
  const gpu = limit(input.generationMaxGPUProcesses);
  return {
    generationMarkerBackend: backend(input.generationMarkerBackend),
    generationSpriteBackend: backend(input.generationSpriteBackend),
    generationDevice: /^\/dev\/dri\/renderD[0-9]+$/.test(
      input.generationDevice ?? ""
    )
      ? input.generationDevice
      : "/dev/dri/renderD128",
    generationBudgetEnabled: input.generationBudgetEnabled ?? false,
    generationMaxProcesses: maxProcesses,
    generationMaxGPUProcesses: gpu,
    generationThreads: limit(input.generationThreads),
  };
}
