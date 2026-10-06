# Generation configuration

Settings → System → FFmpeg generation and the YAML settings select independent scene preview, marker and sprite backends. Existing installations default to software. Saved requests take effect after an authorized restart; saving does not replace the running scheduler or restart the app.

```yaml
generation:
  previews:
    backend: software # software or vaapi
  markers:
    backend: software # software or vaapi
  sprites:
    backend: software # software or vaapi
  device: /dev/dri/renderD128
  budget:
    enabled: false
    processes: 1
    gpu_processes: 1
    threads: 1
```

Selecting VAAPI requires GPU video decode, scaling/color conversion, composition where applicable, and encoding. There is no automatic software rendering fallback. Unsupported source metadata, device access, missing filters/encoders or failed hardware execution produce an actionable job error. Stored QSV requests remain readable but fail explicitly because the complete resident output path is unavailable; the GUI disables new QSV selection. Select software explicitly for CPU rendering.

Scene and marker MP4 use `scale_vaapi` and `h264_vaapi`. JPEG sprite sheets use GPU composition and `mjpeg_vaapi` and bypass BMP export, Go image composition and Go JPEG encoding. GPU marker JPEG stills use the same resident scale/JPEG path. Right-angle rotation and reflections use VAAPI. Supported VR projection and HDR tone/gamut conversion use Vulkan/libplacebo on the same selected device, then import GPU RGB surfaces directly into VAAPI for final scaling/composition/encoding. Ten-bit sources retain a ten-bit RGB intermediate. The image pins and checks the GPU interoperability, strict hardware decoder and pixel-free stream-probing changes described in [the FFmpeg README](../../scripts/ffmpeg/README.md). GPU source discovery parses headers and recovers actual geometry/color/precision from a bounded first hardware frame before planning. A custom FFmpeg or library without the required options fails explicitly. Lossless animated WebP has no supported GPU encoder in the installed Intel/FFmpeg stack; a GPU request fails without changing format or quality. Unsupported formats or capabilities also fail explicitly. Literal zero CPU usage is impossible: I/O, demuxing, metadata/timeline selection, orchestration, muxing and optional AAC audio remain CPU work. See [scene previews](scene-previews.md) and [resident sprites](resident-sprites.md) for source eligibility and acceptance evidence.

Intel probes and execution always share the configured total/GPU process limits. Enabling the shared budget also applies them to explicitly selected CPU generation and canonical CPU pHash. Missing, zero and `auto` numeric limits request [runtime Auto sizing](auto-capacity.md). Explicit nonnegative integer limits are preserved; an explicit GPU limit cannot exceed an explicit total limit. There is no fixed 64-setting ceiling. No backend introduces a separate fixed concurrency cap. These are process/thread limits, not OS CPU or RAM quotas. Capability probe execution deadlines begin after admission; failures and cancellation release permits and drain children before cleanup. Coordinators do not hold nested permits. Intel selection bypasses the separate native device pools.

The Settings controls are drafts until Save generation settings succeeds. The UI shows server-confirmed saved requests separately from the running snapshot and flags pending restart changes. The complete proposal is validated before any field is persisted, including coupled process limits. Command-line overrides cannot be overwritten. Invalid startup configuration is rejected; correct it before restarting. A render device must be an explicit `/dev/dri/renderD<number>` path. Direct YAML edits require an authorized restart. These controls do not authorize production configuration changes or restart/deployment.

`ConfigGeneralInput` accepts `generationMarkerBackend`, `generationSpriteBackend`, `generationPreviewBackend`, `generationDevice`, `generationBudgetEnabled`, `generationMaxProcesses`, `generationMaxGPUProcesses` and `generationThreads`. Partial API proposals merge with saved requests for atomic validation. `ConfigGeneralResult` returns saved requests, frozen `activeGeneration`, `generationRestartRequired` and `generationConfigurationError`. Backend names describe requests; runtime diagnostics identify actual execution and failure stages.

Select software rollback, save and restart when authorized to restore CPU rendering and legacy scheduling. Stored pHashes remain unchanged; Intel GPU pHash stays unavailable until its independent exact-parity gate is satisfied. Use the [existing benchmark tools](benchmark.md) and [B580 runbook](b580-acceptance.md) for isolated authorized tests. Historical [Stage A evidence](ct102-stage-a.md) describes the former hybrid implementation and does not establish acceptance of the resident pipeline.
