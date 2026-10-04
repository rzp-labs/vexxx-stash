# Opt-in generation candidate

The candidate exposes generation requests in **Settings → System → FFmpeg generation (experimental Intel)** and in the YAML configuration below. They are independent of playback hardware settings and Windows/AMD Native Generation. No hardware backend or CPU budget is enabled by migrating an existing configuration.

```yaml
generation:
  markers:
    backend: software # software, vaapi; stored qsv marker requests use CPU fallback
  sprites:
    backend: software # software, vaapi, qsv
  device: /dev/dri/renderD128
  budget:
    enabled: false
    processes: 1
    gpu_processes: 1
    threads: 1
```

Explicit Intel selection always enables the shared budget, even when `enabled` is false. Missing, zero and `auto` numeric values mean one. Limits must be integers in 0..64, and GPU process count cannot exceed total process count. The device must be a literal `/dev/dri/renderD<number>` path; capability probes check its actual availability and permissions. Invalid configuration fails validation. If a caller bypasses validation, generation conservatively uses software with a one-process budget and an actionable warning.

The settings are snapshotted for the configuration's lifetime. The Settings controls are drafts until **Save generation settings** succeeds. The UI shows server-confirmed saved backends separately from the running configuration; a saved request that differs from the startup snapshot displays a pending restart notice. Saving never restarts the application or replaces its active scheduler. The full proposed configuration is validated before any generation field is applied, including coupled GPU/total limits. Invalid device paths, backend values, booleans and limits are rejected; command-line overrides cannot be overwritten. Direct YAML edits should be made while generation is stopped, then require an authorized application restart. Replacing a scheduler during active jobs could otherwise admit work against two independent budgets. These settings do not authorize a production restart or Unraid access.

For CT102 candidate experiments, select one workload at a time, `processes: 1`, `gpu_processes: 1`, `threads: 1`; retain its existing cgroup limits. These are conservative laboratory controls, not agreed B580 acceptance budgets or an OS CPU quota. FFmpeg decoder/filter/encoder thread limits can each create work; one does not imply total process CPU <= one core.

Generation stages share cancellable FIFO admission. The permit covers probes, individual FFmpeg operations, canonical CPU pHash and sprite composition/encoding. Hardware probe execution timeout starts after admission. Failed hardware work releases its permit before one CPU retry. Animated WebP retains the lossless/compression-6 encoder and remains CPU work. VR remains on its existing software projection path. An explicit shared budget bypasses the existing native AMF pipeline, whose separate device pools do not share these controls. Images and unrelated scan/plugin processing are not all covered by this media budget.

Current Intel marker eligibility is deliberately conservative: H.264/HEVC 8-bit 4:2:0 SDR, a single video stream, unambiguous audio selection, no rotation, square sample aspect ratio, no VR projection. The sprite path additionally requires known duration >=5 seconds and matching positive frame-rate declarations; it preserves independent accurate timestamp seeks rather than introducing new batch sampling. Actual decode, scale, transfer and encode probes select the requested device. Only the tested combinations in the accompanying CT102 evidence are validated; successful probing alone does not establish visual compatibility for a new corpus.

VAAPI marker quality uses candidate `qp=24`, which is not equivalent to x264 CRF24/veryslow. QSV marker quality did not pass the laboratory quality gate, so the marker QSV option is disabled in the GUI. Existing API/YAML `qsv` marker requests remain readable and use the canonical CPU fallback with a quality-gate reason; QSV sprites remain selectable. Compare quality, color, size and browser playback before adopting a validated hardware combination. Structured diagnostics expose selected/actual backend, stage and reason; fallback cannot be counted as GPU execution.

To roll back after authorized testing, use **Select software rollback** and **Save generation settings**, then restart when authorized. This selects `software` for markers and sprites and disables the budget, restoring legacy scheduling after restart. Selecting rollback only changes the draft; saving confirms the persisted request. The currently running backend remains visible until restart. Stored pHashes are never rehashed automatically. Intel GPU pHash is unavailable: its exact parity and worthwhile resource benefit gates remain closed. Canonical CPU pHash with the budget uses one decoder at a time and preserves the existing seek, scale and hash algorithm.

Use [the benchmark harness](benchmark.md) and [B580 acceptance runbook](b580-acceptance.md). CT102 evidence is a laboratory candidate result. B580 validation requires the user's explicit parity-completion confirmation and scoped authorization.

## API and diagnostics

`ConfigGeneralInput` accepts `generationMarkerBackend`, `generationSpriteBackend`, `generationDevice`, `generationBudgetEnabled`, `generationMaxProcesses`, `generationMaxGPUProcesses` and `generationThreads`. The Settings UI saves these as one complete proposal. API clients may send partial proposals; validation merges them with the existing requested settings before applying anything. `ConfigGeneralResult` returns those persisted requests, `activeGeneration` (the frozen running configuration and effective budget), `generationRestartRequired` and `generationConfigurationError` for invalid YAML. Startup deliberately validates the persisted generation request and refuses malformed YAML; correct the configuration file before restarting. The error field and conservative snapshot fallback describe invalid requested settings observed by an already initialized instance, not a startup recovery mode. Backend names remain requests; the active snapshot is not proof of actual device activity.

Per-job FFmpeg diagnostics identify selected and actual backend, failure/probe stage and fallback reason in the application logs. The UI explains this distinction and that WebP encoding and VR projection remain CPU work. This candidate does not claim Native Generation, playback or GPU pHash capability from the Intel FFmpeg controls.
