import type { ConfigGeneralInput } from "src/core/generated-graphql";

export function validateGenerationSettings(input: ConfigGeneralInput) {
  if (
    !["software", "qsv", "vaapi"].includes(
      input.generationMarkerBackend ?? ""
    ) ||
    !["software", "qsv", "vaapi"].includes(
      input.generationSpriteBackend ?? ""
    ) ||
    !["software", "vaapi"].includes(
      input.generationPreviewBackend ?? "software"
    )
  ) {
    return "config.general.generation.invalid_backend";
  }
  if (!/^\/dev\/dri\/renderD[0-9]+$/.test(input.generationDevice ?? "")) {
    return "config.general.generation.invalid_device";
  }
  // GraphQL Int is signed32-bit; this is a transport bound, not a generation ceiling.
  const limits = [
    input.generationMaxProcesses,
    input.generationMaxGPUProcesses,
    input.generationThreads,
  ];
  if (
    limits.some(
      (v) => v == null || !Number.isInteger(v) || v < 0 || v > 2 ** 31 - 1
    )
  ) {
    return "config.general.generation.invalid_limits";
  }
  if (
    input.generationMaxProcesses! > 0 &&
    input.generationMaxGPUProcesses! > input.generationMaxProcesses!
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
    value != null &&
    Number.isInteger(value) &&
    value >= 0 &&
    value <= 2 ** 31 - 1
      ? value
      : 0;
  const maxProcesses = limit(input.generationMaxProcesses);
  const gpu = limit(input.generationMaxGPUProcesses);
  return {
    generationMarkerBackend: backend(input.generationMarkerBackend),
    generationSpriteBackend: backend(input.generationSpriteBackend),
    generationPreviewBackend: ["software", "vaapi"].includes(
      input.generationPreviewBackend ?? ""
    )
      ? input.generationPreviewBackend
      : "software",
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
