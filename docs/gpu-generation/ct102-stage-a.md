# CT102 laboratory candidate

This opt-in candidate has bounded synthetic CT102 functional evidence. Production release and B580 acceptance remain pending. Use the exact source-tree, driver SHA256, manifest and raw/enriched results delivered with the candidate; an uncommitted Git tree is not a commit. No production media or Unraid access was used.

## Supported candidate paths

| Workload | Candidate selection | Current behavior |
| --- | --- | --- |
| MP4 markers | VAAPI | Explicit-device decode/scale/H.264 encode; single CPU fallback on unsupported source or failed stage; output requires real video packets before publication |
| MP4 markers | QSV | Quality gate returns canonical CPU output with an explicit `quality` diagnostic; GUI option disabled |
| Scrubber sprites | VAAPI / QSV | Per-timestamp accurate seek and GPU scale, small bitmap transfer, canonical CPU montage/VTT; whole-sheet CPU fallback once |
| Animated WebP / VR | software | Corrected explicit lossless/compression-6 encoder; canonical projection, 12fps, five seconds and infinite loop retained |
| pHash | software | Canonical 25-frame CPU algorithm, shared budget and bounded decoding; Intel GPU pHash unavailable |

Eligibility remains conservative: single-video H.264/HEVC 8-bit SDR, no rotation/non-square aspect/VR. Sprites also require supported duration and compatible frame-rate declarations. Declarations alone do not prove arbitrary VFR equivalence. HDR/10-bit and VR marker inputs fall back to CPU. EOF without actual video packets is an error and cannot replace an existing marker. Existing software defaults are unchanged except for the necessary WebP preset correction.

## Resource observations

Under the existing two-CPU/two-GiB CT102 limits, the shared budget used one process/GPU slot and one configured thread. Two short observations of 81-tile generation used about 17.6 CPU seconds for software, 8.2 for VAAPI and 10.0 for QSV. Sprite VTTs were byte-identical. These are short synthetic observations, not sustained benchmarks or agreed acceptance thresholds.

Correcting FFmpeg's named WebP preset exposes its real lossless cost: the 1080p synthetic five-second marker used about 21.4 CPU seconds and 96 MiB peak RSS. The previous output was lossy despite the flag and took about 1.4 CPU seconds; it must not be used as a lossless baseline. Tiny six-frame artifacts grew approximately sevenfold. Full mixed queues including corrected WebP, marker, 81 sprites and CPU pHash completed in approximately 36–37 seconds with one sampled media child, zero after drain and a reusable budget. A one-second cancellation drained all coordinators. CLI scheduling delay is not application UI or playback latency.

Provisional lab guardrails are the existing cgroup limits, one media/GPU slot, one configured thread, 50-second driver/55-second case bounds, no surviving children and successful permit reuse. Mixed-driver fixtures are limited to 10-second 30-fps 16:9 8-bit inputs of at most 1920×1080, with a separate exact 640×360 CPU hash fixture. Missing, partial or failed final procfs sampling leaves mixed process-limit acceptance untested; sampled child counts remain lower bounds. These are safety controls, not user-approved per-workload performance/quality budgets. Lossless WebP consumes substantial CPU even with admission controls; one thread option does not imply an OS one-core quota.

## Quality and compatibility

VAAPI uses explicit candidate `qp=24`; it is not x264 CRF24/veryslow equivalence. Three SDR synthetic comparisons against a CPU Lanczos reference yielded SSIM about .976 (moving saturated pattern), .984 (SMPTE bars), .970 (fine detail/gradient), versus CPU .991/.999/.998. Side-by-side extracted frames preserved obvious geometry, color regions and content; scaling/edge/detail differences remain visible. This limited visual inspection is not human approval or broad color/quality equivalence. Compare size, fine detail, motion, timestamps, color metadata and browser playback on the authorized representative B580 corpus before release. No parameter was tuned to maximize one fixture's score.

An earlier QSV marker setting (`global_quality=24`) scored about .918 on the moving fixture and is gated off. QSV sprites are a distinct path and remain available for candidate tests. Reopening QSV marker generation requires a documented mapping and representative objective plus visual acceptance, with CPU and size tradeoffs recorded.

FFmpeg's `preset=default` overrides explicit lossless and compression-level options regardless of argument order. `preset=none` preserves their intended values. Actual artifact tests verify 60 VP8L frames, no lossy VP8 frames, five-second timing and infinite loop for plain and VR markers. Stored pHashes and GPU pHash gates remain untouched; exact CPU parity on one synthetic 640x360 fixture does not authorize an Intel GPU hash path.

## Isolated application replay

A real Mac application with a task-only database and synthetic 10-second 1080p source completed simultaneous 81-tile sprite and marker MP4/lossless WebP jobs in 49.38 and 32.11 seconds. The configured generation budget was one process/GPU slot and one thread. A bounded 55-second sampler recorded 217 successful read-only job-queue API requests, no request/process-sampling errors, p95 latency 2.38 ms, one peak media child and zero children/jobs after drain. Peak sampled application-plus-child RSS was 234 MiB. Native browser playback, seeking and sprite thumbnails worked during the jobs; a fresh page after completion played with no console warnings/errors. This short synthetic observation does not establish approved responsiveness targets or dropped-frame statistics.

The initial application replay exposed manager metadata probes outside generation admission. Generation source, frame-count and frame-rate probes now acquire the shared CPU permit, release it before extraction, cap execution after admission and propagate cancellation/timeouts. Focused tests cover blocking, release, timeout and legacy scan/playback behavior. The budget applies to generation children; it is not a host-wide quota for scanning or playback. The isolated runtime used official Homebrew FFmpeg 9.0.2 with its full encoder build, including libwebp.

See [configuration controls](configuration.md), [CPU admission limits](cpu-budget.md) and the evidence-recording rules in the acceptance runbook and [B580 acceptance](b580-acceptance.md). GUI save/reload and unit/API failure tests supplement laboratory media evidence. Representative application playback/scrubber responsiveness, broad VFR/rotation/HDR/4K compatibility, human quality acceptance and sustained B580 performance remain release gates.
