# Intel generation candidate readiness and B580 acceptance

This runbook separates CT102 development evidence from B580 release acceptance. The defaults remain software. Per-workload acceleration is opt-in; rollback selects the software backend for that workload. No automatic production enablement or pHash data migration is authorized by these lab steps.

## Stage A: CT102 lab readiness

Use the Mac mini for coordination, edits and hardware-independent checks. Root coordinates CT102 through the existing `prox-svc` alias and Proxmox `pct exec` / transfer. CT102 remains offline and unprivileged: 2 CPUs capped at 2, weight 50, 2 GiB RAM, zero swap, 16-GiB local ZFS root, renderD128 only, autostart off. Do not change networking, resource limits, host drivers or other LXCs, restart the host, add credentials, or use private media. Begin with serialized GPU jobs of at most 55 seconds plus 3 seconds cleanup grace. Stop on host instability, OOM, repeated timeouts or unexpected host resource impact.

Previous 300-frame 1080p 8-bit H.264/HEVC QSV/VAAPI encode/decode checks demonstrate that the lab runtime can perform those operations. They do not validate this candidate or 4K/HDR/10-bit, sustained performance, concurrent queues, visual quality or responsiveness.

For each run record the exact Git candidate SHA (and any uncommitted patch hash), driver/FFmpeg versions, fixture digest/provenance, device, codec/pixel format, command argv/environment, container limits, selected/actual backend, stage/reason, output validation and separate aligned host GPU samples. Store evidence in a new directory and retain earlier evidence.

| Issue | Required candidate evidence | Limit of CT102 evidence |
|---|---|---|
| VEX-1 | Repeated CPU/wall/RSS measurements for canonical CPU and Intel commands; peak/steady CPU; request-latency evidence and budgets | Harness marks responsiveness untested until a real service is measured; B580 budgets unagreed |
| VEX-2 | Separate decode, scale/download and encode probes, explicit device, codec/pixel-format combinations, single visible CPU fallback, stage diagnostics | 1080p 8-bit findings cannot imply other formats or B580 driver support |
| VEX-3 | Shared limits across marker/scene/sprites/filter/encode/fallback; zero/auto/invalid settings; failure/cancel/nested-path permit release | CT102 starts at one GPU job; no sustained/concurrency lab load before limits and bounded queue behavior are proven |
| VEX-4 | MP4 markers match canonical start/duration/max, dimensions/aspect/orientation/color/playback; quality mapping explained; short/EOF/VFR/rotation/10-bit/HDR/cancel/existing-output fixtures | Synthetic 8-bit fixtures alone cannot satisfy broad visual compatibility |
| VEX-5 | Intended lossless WebP compression-6 semantics verified, including correction of the legacy preset override; CPU encode/v360/transfer costs bounded; WebP-only, MP4-only, combined, VR and fallback | Positive GPU decode activity does not remove CPU WebP/projection costs |
| VEX-6 | Opt-in per-workload backend/device/budget persistence, defaults/migration/toggles/cancel/rollback; selected/actual backend and fallback UI | No production auto-enable or unsupported Native Generation/playback claims |
| VEX-7 | Exact candidate, all checks/review repaired, marker support matrix, duration/geometry/color/quality/playback evidence, repeated resource/headroom results | Stage A lab readiness can be partial while Stage B acceptance remains deferred |
| VEX-8 | Sprites preserve timestamp sampling/count/order, geometry/orientation, montage/VTT intervals/coordinates; batch/seek/transfer comparison; cleanup/fallback; short/VFR/long-GOP/HDR/rotation/8K VR | Do not accelerate until canonical sprite fixtures and exact VTT comparison pass |
| VEX-9 | Real scrubber behavior, canonical layout/VTT, bounded mixed queue without starvation; missing GPU/failure/cancel/restart/rollback and marker regression | Hardware-independent queue tests can precede bounded service measurements |
| VEX-10 | Strict pHash golden corpus and intermediate pixels/frame diagnostics; repeated exact hash equality; canonical CPU scale/hash after GPU decode; measured savings | Preparing corpus is allowed; approximate equality or visual similarity is a no-go |
| VEX-11 | Enable only proven pHash combinations after VEX-10 go, bounded canonical CPU fallback and unchanged stored hashes | Keep CPU pHash unless exact parity and useful savings are both demonstrated |

Run focused unit/integration checks appropriate to modified code, then the final backend tests/build and frontend type/lint checks required by the change. An independent reviewer checks the final candidate and the test evidence; repair findings and rerun affected checks. Preserve existing user changes. Publication remains separate authorization; do not push, merge, deploy or create a PR until the parent confirms permission.

## Stage B: B580 acceptance (deferred)

No Unraid access is permitted before the parent provides the user's parity-complete confirmation and access scope. Once authorized, use the exact reviewed Stage A candidate and rollback config. Start by inspecting only the scoped runtime/device/version information and available resource headroom. Probe supported decode/filter/encode combinations afresh on B580; an iGPU pass is not an Arc support claim.

Agree per-workload acceptance budgets before release decisions: representative fixture class and sample size, wall time, user+system CPU seconds, peak/steady CPU, peak RSS, service responsiveness, cancellation latency, quality/playback tolerances and fallback behavior. Record idle and CPU-only controls under the same limits/settings. Run repeated short isolated jobs first; broaden formats and mixed queues only after resource/cleanup stability is confirmed. Sustained testing requires separately approved bounds, stop conditions and host-headroom limits.

Compare original and generated marker MP4/WebP playback side by side using authorized fixtures. Check first/last frames, EOF/short duration, VFR timing, rotations, aspect/geometry, color/HDR handling, format/codec and actual browser/player behavior. Verify corrected lossless/compression-6 WebP and unchanged VR projection/timing behavior. Do not compare resource cost against the earlier lossy output as a lossless baseline. QSV marker previews are gated to CPU pending quality mapping acceptance; QSV sprites remain a separate candidate path. Keep separate support-matrix cells for decode, filter/projection/transfer and encode; a failed stage falls back once with a visible reason.

For sprites, compare canonical timestamp lists, selected source frames, count/order, tile geometry/montage, VTT start/end intervals and coordinates exactly where canonical semantics demand equality. Open the actual scrubber and inspect boundaries, seeking through long-GOP/VFR and the final tile. Check no duplicates, missing tiles, timestamp drift or stale files after cancel/restart. Recheck marker paths in the same candidate.

### QSV single-frame timestamp regression

QSV sprite screenshots set the input decoder's `-async_depth 1`. In the bounded
B580 H.264 experiment, FFmpeg 8.1.2's default depth returned source pixels at
1.900 seconds while reporting the timestamp for 2.000 seconds. The discrepancy
was present in a full-resolution downloaded frame before hardware scaling.
Depth one returned the intended 2.000-second frame in both the scaled screenshot
and decode/download controls, with the same software control. This validates
one synthetic 1080p 8-bit seek, not all QSV media or complete sprite acceptance.

When the next bounded hardware regression is authorized, retain lossless
software and QSV captures plus decoder/filter timestamp diagnostics at the same
requested seek. Check actual source-frame pixel identity before and after the
QSV scaler, using neighboring canonical source frames as references. Restore
the input seek offset when interpreting normalized PTS; VPP can change the
output timebase. Matching VTT bytes or timestamp labels alone cannot establish
correct frame selection. Keep the decoder option scoped to QSV screenshots;
shared capability probes and marker/software/VAAPI paths retain their settings.

Before broader acceptance, repeat on approved seek positions and codec/fixture
classes and compare the complete 81-tile sequence. If source-frame identity
fails, select software fallback for QSV sprites. Do not compensate with a fixed
seek offset or sprite re-indexing. Keep the independent pHash gate closed.

pHash is gated separately. Capture canonical and candidate decoded/scaled/grayscale frames with timestamps/order and diagnostic pixel hashes. Require exact final hash equality on every approved corpus case across repeated runs, preserving seek/frame order/montage/color semantics and current algorithm. No jitter tolerance, substituted last frames, library rehash, changed algorithm or stored-hash migration. If any mismatch or no worthwhile resource savings is observed, retain canonical CPU pHash. Only proven combinations can receive opt-in acceleration; other combinations visibly use canonical CPU fallback.

## Rollback and evidence handoff

Select software for the affected workload, restore prior generation settings, cancel/finish bounded jobs and confirm shared permits were released. Verify canonical CPU MP4/WebP/sprites/pHash and cleanup; retain unchanged source fixtures and stored hashes. Avoid deleting shared output directories. Record exact candidate, rollback settings, failed stage/reason and new isolated evidence artifact paths.

Report issue-mapped outcomes as `passed`, `failed`, `untested` or `blocked` with the relevant artifact, candidate SHA and support matrix. A capability probe, a successful exit, a selected backend, or observed video-engine activity alone must never mark a workload or release accepted.
