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
